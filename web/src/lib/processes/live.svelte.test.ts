import { create, type MessageInitShape } from '@bufbuild/protobuf';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { Code } from '#lib/api/client.js';
import { WatchProcessesResponseSchema } from '#lib/api/gen/sermon/web/v1/live_view_pb.js';
import { liveViewApi } from '#lib/api/liveview.js';
import { rpcError } from '#lib/test/fakes.js';
import { ProcessViewState } from './live.svelte.js';

type Response = MessageInitShape<typeof WatchProcessesResponseSchema>;

const node = '0193f5a0-0000-7000-8000-000000000001';
const fast = { retryFirst: 20, retryMax: 40, shortStream: 10_000 };

function snapshot(pid: number): Response {
	return { event: { case: 'snapshot', value: { processes: [{ pid, name: `p${pid}` }] } } };
}

const pids = (state: ProcessViewState) => state.snapshot?.processes.map((p) => p.pid);

/** A stream the test feeds message by message; it ends with end() or fails with fail(). */
function controlled() {
	let push: (r: Response | Error | null) => void = () => {};
	const spy = vi.spyOn(liveViewApi.liveView, 'watchProcesses').mockImplementation(((
		_r: unknown,
		options?: { signal?: AbortSignal }
	) =>
		(async function* () {
			while (!options?.signal?.aborted) {
				const next = await new Promise<Response | Error | null>((done) => {
					push = done;
					options?.signal?.addEventListener('abort', () => done(null));
				});
				if (next === null) return;
				if (next instanceof Error) throw next;
				yield create(WatchProcessesResponseSchema, next);
			}
		})()) as never);
	return { spy, send: (r: Response) => push(r), end: () => push(null) };
}

describe('ProcessViewState', () => {
	afterEach(() => vi.restoreAllMocks());

	it('shows the newest snapshot and the node being offline', async () => {
		const stream = controlled();
		const state = new ProcessViewState(fast);
		state.start(node);
		expect(state.status).toBe('connecting');
		expect(stream.spy.mock.calls[0][0]).toEqual({ nodeId: node });
		stream.send(snapshot(1));
		await vi.waitFor(() => expect(state.status).toBe('live'));
		expect(pids(state)).toEqual([1]);
		stream.send({ event: { case: 'offline', value: true } });
		await vi.waitFor(() => expect(state.status).toBe('offline'));
		// The last snapshot stays visible until the agent is back.
		expect(pids(state)).toEqual([1]);
		stream.send(snapshot(2));
		await vi.waitFor(() => expect(state.status).toBe('live'));
		expect(pids(state)).toEqual([2]);
		state.stop();
		expect(state.status).toBe('stopped');
	});

	it('keeps the shown snapshot while paused and shows the newest on resume', async () => {
		const stream = controlled();
		const state = new ProcessViewState(fast);
		state.start(node);
		stream.send(snapshot(1));
		await vi.waitFor(() => expect(pids(state)).toEqual([1]));
		state.pause();
		stream.send(snapshot(2));
		await vi.waitFor(() => expect(state.waiting).toBe(true));
		stream.send(snapshot(3));
		await new Promise((done) => setTimeout(done, 20));
		expect(pids(state)).toEqual([1]);
		state.resume();
		expect(pids(state)).toEqual([3]);
		expect(state.waiting).toBe(false);
		expect(state.paused).toBe(false);
		state.stop();
	});

	it('ends with the reason of a refusal and does not reconnect', async () => {
		const spy = vi.spyOn(liveViewApi.liveView, 'watchProcesses').mockImplementation((() =>
			(async function* () {
				yield create(WatchProcessesResponseSchema, {
					event: { case: 'refused', value: 'allow_live_view = false' }
				});
			})()) as never);
		const state = new ProcessViewState(fast);
		state.start(node);
		await vi.waitFor(() => expect(state.status).toBe('refused'));
		expect(state.reason).toBe('allow_live_view = false');
		await new Promise((done) => setTimeout(done, 100));
		expect(spy).toHaveBeenCalledTimes(1);
	});

	it.each([
		[Code.PermissionDenied, 'forbidden'],
		[Code.NotFound, 'not_found'],
		[Code.ResourceExhausted, 'too_many']
	])('gives up on %s', async (code, failure) => {
		const spy = vi.spyOn(liveViewApi.liveView, 'watchProcesses').mockImplementation((() =>
			(async function* () {
				yield* [];
				throw rpcError(code);
			})()) as never);
		const state = new ProcessViewState(fast);
		state.start(node);
		await vi.waitFor(() => expect(state.status).toBe('failed'));
		expect(state.failure).toBe(failure);
		await new Promise((done) => setTimeout(done, 100));
		expect(spy).toHaveBeenCalledTimes(1);
	});

	it('stops quietly after a logout', async () => {
		vi.spyOn(liveViewApi.liveView, 'watchProcesses').mockImplementation((() =>
			(async function* () {
				yield* [];
				throw rpcError(Code.Unauthenticated);
			})()) as never);
		const state = new ProcessViewState(fast);
		state.start(node);
		await vi.waitFor(() => expect(state.status).toBe('stopped'));
		expect(state.failure).toBeUndefined();
	});

	it('reconnects after an outage, keeping the last snapshot', async () => {
		let call = 0;
		const spy = vi.spyOn(liveViewApi.liveView, 'watchProcesses').mockImplementation(((
			_r: unknown,
			options?: { signal?: AbortSignal }
		) => {
			call++;
			const mine = call;
			return (async function* () {
				yield create(WatchProcessesResponseSchema, snapshot(mine));
				if (mine === 1) throw rpcError(Code.Unavailable);
				await new Promise((done) => options?.signal?.addEventListener('abort', done));
			})();
		}) as never);
		const state = new ProcessViewState(fast);
		state.start(node);
		await vi.waitFor(() => expect(pids(state)).toEqual([2]));
		expect(spy).toHaveBeenCalledTimes(2);
		expect(state.status).toBe('live');
		// M3-28: the first open is the user's, the reopen marked as the UI's own (no activity).
		const headers = spy.mock.calls.map(
			(c) => (c[1] as { headers?: Record<string, string> })?.headers
		);
		expect(headers[0]).toBeUndefined();
		expect(headers[1]).toEqual({ 'Sermon-Background': '1' });
		state.stop();
	});

	it('reconnects at once after a long stream, and waits after one that ends at once', async () => {
		const spy = vi.spyOn(liveViewApi.liveView, 'watchProcesses').mockImplementation((() =>
			(async function* () {
				yield* [];
				await new Promise((done) => setTimeout(done, 30));
			})()) as never);
		const state = new ProcessViewState({ retryFirst: 300, retryMax: 300, shortStream: 10_000 });
		state.start(node);
		await new Promise((done) => setTimeout(done, 200));
		expect(spy).toHaveBeenCalledTimes(1);
		state.stop();

		spy.mockClear();
		// Streams of 30 ms count as long here: the next one opens without the wait of 300 ms.
		const long = new ProcessViewState({ retryFirst: 300, retryMax: 300, shortStream: 20 });
		long.start(node);
		await new Promise((done) => setTimeout(done, 200));
		expect(spy.mock.calls.length).toBeGreaterThan(3);
		long.stop();
	});

	it('starts once and again after a stop', () => {
		const stream = controlled();
		const state = new ProcessViewState(fast);
		state.start(node);
		state.start(node);
		expect(stream.spy).toHaveBeenCalledTimes(1);
		state.stop();
		state.start(node);
		expect(stream.spy).toHaveBeenCalledTimes(2);
		state.stop();
	});
});
