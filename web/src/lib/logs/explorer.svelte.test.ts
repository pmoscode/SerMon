import { create, type MessageInitShape } from '@bufbuild/protobuf';
import {
	durationFromMs,
	timestampFromMs,
	timestampMs,
	type Timestamp
} from '@bufbuild/protobuf/wkt';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { Code } from '#lib/api/client.js';
import {
	GetLogContextResponseSchema,
	GetLogHistogramResponseSchema,
	LogLineSchema,
	LogSource,
	QueryLogsResponseSchema,
	TailLogsRequestSchema,
	TailLogsResponseSchema,
	type LogLine
} from '#lib/api/gen/sermon/web/v1/log_pb.js';
import { LogSeverity } from '#lib/api/gen/sermon/web/v1/log_settings_pb.js';
import { logsApi } from '#lib/api/logs.js';
import { rpcError } from '#lib/test/fakes.js';
import { LogExplorerState } from './explorer.svelte.js';
import { MAX_LINES, merge } from './lines.js';
import { emptyFilter, type LogFilter } from './filter.js';

type TailRequest = MessageInitShape<typeof TailLogsRequestSchema>;
type TailResponse = MessageInitShape<typeof TailLogsResponseSchema>;
const ms = (t: unknown) => timestampMs(t as Timestamp);

const node = '0193f5a0-0000-7000-8000-000000000001';
const now = Date.now();

function line(msAgo: number, message: string): LogLine {
	return create(LogLineSchema, {
		nodeId: node,
		hostname: 'web-01',
		at: timestampFromMs(now - msAgo),
		severity: LogSeverity.ERROR,
		source: LogSource.JOURNAL,
		unit: 'nginx.service',
		message
	});
}

const filter: LogFilter = { ...emptyFilter(), severity: 'warning', units: ['nginx.service'] };

function queried(lines: LogLine[], more = false) {
	return create(QueryLogsResponseSchema, { lines, more });
}

function stubHistogram() {
	return vi.spyOn(logsApi.logs, 'getLogHistogram').mockResolvedValue(
		create(GetLogHistogramResponseSchema, {
			step: durationFromMs(36_000),
			series: [
				{
					severity: LogSeverity.ERROR,
					points: [{ at: timestampFromMs(now - 60_000), count: 4 }]
				}
			]
		})
	);
}

/** Tails that send the given batches, then wait until they are aborted. */
function tailOf(...batches: TailResponse[][]) {
	const requests: TailRequest[] = [];
	let call = 0;
	const spy = vi.spyOn(logsApi.logs, 'tailLogs').mockImplementation(((
		request: TailRequest,
		options?: { signal?: AbortSignal }
	) => {
		requests.push(request);
		const mine = batches[call++] ?? [];
		return (async function* () {
			for (const b of mine) yield create(TailLogsResponseSchema, b);
			await new Promise((done) => options?.signal?.addEventListener('abort', done));
		})();
	}) as never);
	return { spy, requests };
}

describe('LogExplorerState', () => {
	afterEach(() => vi.restoreAllMocks());

	it('queries the lines and the histogram of the filter', async () => {
		const query = vi
			.spyOn(logsApi.logs, 'queryLogs')
			.mockResolvedValue(queried([line(2000, 'b'), line(1000, 'a')], true));
		stubHistogram();
		const state = new LogExplorerState();
		await state.search({ ...filter, nodes: [node] }, undefined, now);

		const request = query.mock.calls[0][0].filter!;
		expect(request.nodeIds).toEqual([node]);
		expect(request.minSeverity).toBe(LogSeverity.WARNING);
		expect(request.units).toEqual(['nginx.service']);
		expect(ms(request.to!) - ms(request.from!)).toBe(3600_000);
		expect(state.status).toBe('ready');
		expect(state.lines.map((l) => l.message)).toEqual(['a', 'b']);
		expect(state.more).toBe(true);
		expect(state.histogram?.step).toBe(36_000);
		expect(state.histogram?.series[0].points[0].count).toBe(4);
	});

	it('uses the node of a node page instead of the node filter', async () => {
		const query = vi.spyOn(logsApi.logs, 'queryLogs').mockResolvedValue(queried([]));
		stubHistogram();
		await new LogExplorerState().search({ ...filter, group: node, nodes: ['x'] }, node, now);
		expect(query.mock.calls[0][0].filter!.nodeIds).toEqual([node]);
		expect(query.mock.calls[0][0].filter!.groupId).toBeUndefined();
	});

	it('says when Loki is unavailable or the filter is refused', async () => {
		stubHistogram();
		const query = vi.spyOn(logsApi.logs, 'queryLogs');
		const state = new LogExplorerState();
		query.mockRejectedValueOnce(rpcError(Code.Unavailable));
		await state.search(filter, undefined, now);
		expect(state.status).toBe('error');
		expect(state.error).toMatch(/Loki|log store|Log-Speicher/i);
		query.mockRejectedValueOnce(rpcError(Code.PermissionDenied));
		await state.search(filter, undefined, now);
		expect(state.error).not.toBe('');
	});

	it('loads older pages without duplicates', async () => {
		const first = [line(1000, 'c'), line(2000, 'b')];
		const query = vi
			.spyOn(logsApi.logs, 'queryLogs')
			.mockResolvedValueOnce(queried(first, true))
			.mockResolvedValueOnce(queried([line(2000, 'b'), line(3000, 'a')], false));
		stubHistogram();
		const state = new LogExplorerState();
		await state.search(filter, undefined, now);
		await state.loadMore();
		const to = query.mock.calls[1][0].filter!.to!;
		expect(to.seconds).toBe(first[1].at!.seconds);
		expect(to.nanos).toBe(first[1].at!.nanos + 1);
		expect(state.lines.map((l) => l.message)).toEqual(['c', 'b', 'a']);
		expect(state.more).toBe(false);
	});

	it('asks for the context of a line', async () => {
		const spy = vi
			.spyOn(logsApi.logs, 'getLogContext')
			.mockResolvedValue(create(GetLogContextResponseSchema, { lines: [line(1, 'x')] }));
		const l = line(5, 'y');
		const lines = await new LogExplorerState().context(l);
		expect(spy.mock.calls[0][0]).toMatchObject({ nodeId: node, unit: 'nginx.service' });
		expect(lines).toHaveLength(1);
	});

	it('adds tailed lines on top, holds them while paused and shows them on resume', async () => {
		vi.spyOn(logsApi.logs, 'queryLogs').mockResolvedValue(queried([line(60_000, 'old')]));
		stubHistogram();
		const { requests } = tailOf([
			{ lines: [] },
			{ lines: [line(500, 'new 1')] },
			{ lines: [line(400, 'new 2'), line(300, 'new 3')], throttled: true }
		]);
		const state = new LogExplorerState();
		await state.search(filter, undefined, now);
		state.pause(); // not live yet: nothing to pause
		expect(state.paused).toBe(false);
		state.startTail();
		await vi.waitFor(() => expect(state.lines.map((l) => l.message)).toContain('new 3'));
		expect(state.lines.map((l) => l.message)).toEqual(['new 3', 'new 2', 'new 1', 'old']);
		expect(state.live).toBe('live');
		expect(state.throttled).toBe(true);
		expect(requests[0].minSeverity).toBe(LogSeverity.WARNING);
		// The newest line was a minute ago: the tail starts there.
		expect(ms(requests[0].since!)).toBe(now - 60_000);
		state.stopTail();
		expect(state.live).toBe('off');
	});

	it('buffers while paused', async () => {
		vi.spyOn(logsApi.logs, 'queryLogs').mockResolvedValue(queried([]));
		stubHistogram();
		let push: (r: TailResponse) => void = () => {};
		vi.spyOn(logsApi.logs, 'tailLogs').mockImplementation(((
			_r: unknown,
			options?: { signal?: AbortSignal }
		) =>
			(async function* () {
				while (!options?.signal?.aborted) {
					yield create(
						TailLogsResponseSchema,
						await new Promise<TailResponse>((done) => (push = done))
					);
				}
			})()) as never);
		const state = new LogExplorerState();
		await state.search(filter, undefined, now);
		state.startTail();
		await vi.waitFor(() => expect(state.live).toBe('connecting'));
		push({ lines: [line(100, 'one')] });
		await vi.waitFor(() => expect(state.lines).toHaveLength(1));
		state.pause();
		push({ lines: [line(50, 'two'), line(40, 'three')] });
		await vi.waitFor(() => expect(state.pending).toBe(2));
		expect(state.lines).toHaveLength(1);
		state.resume();
		expect(state.lines.map((l) => l.message)).toEqual(['three', 'two', 'one']);
		expect(state.pending).toBe(0);
		state.destroy();
	});

	it('waits before reconnecting a tail that ends at once', async () => {
		vi.spyOn(logsApi.logs, 'queryLogs').mockResolvedValue(queried([]));
		stubHistogram();
		const spy = vi
			.spyOn(logsApi.logs, 'tailLogs')
			.mockImplementation((() => (async function* () {})()) as never);
		const state = new LogExplorerState();
		await state.search(filter, undefined, now);
		state.startTail();
		await new Promise((done) => setTimeout(done, 500));
		expect(spy).toHaveBeenCalledTimes(1);
		state.destroy();
	});

	it('reconnects after a normal end and after an outage, and stops when refused', async () => {
		vi.spyOn(logsApi.logs, 'queryLogs').mockResolvedValue(queried([]));
		stubHistogram();
		let call = 0;
		const spy = vi.spyOn(logsApi.logs, 'tailLogs').mockImplementation((() => {
			call++;
			return (async function* () {
				yield create(TailLogsResponseSchema, { lines: [line(100 - call, `line ${call}`)] });
				if (call === 2) throw rpcError(Code.Unavailable);
				if (call === 3) throw rpcError(Code.ResourceExhausted);
			})();
		}) as never);
		const state = new LogExplorerState();
		await state.search(filter, undefined, now);
		state.startTail();
		await vi.waitFor(() => expect(state.live).toBe('off'), { timeout: 8000 });
		expect(spy).toHaveBeenCalledTimes(3);
		expect(state.liveError).not.toBe('');
		expect(state.lines).toHaveLength(3);
		// The reconnects continue at the newest line.
		const since = spy.mock.calls[2][0].since!;
		expect(ms(since)).toBe(now - 98);
		// M3-28: only the first open is the user's; the reopens are marked as the UI's own.
		const headers = spy.mock.calls.map(
			(c) => (c[1] as { headers?: Record<string, string> } | undefined)?.headers
		);
		expect(headers[0]).toBeUndefined();
		expect(headers.slice(1)).toEqual([{ 'Sermon-Background': '1' }, { 'Sermon-Background': '1' }]);
	});

	it('follows a new filter with the running tail', async () => {
		vi.spyOn(logsApi.logs, 'queryLogs').mockResolvedValue(queried([]));
		stubHistogram();
		const { requests } = tailOf([], []);
		const state = new LogExplorerState();
		await state.search(filter, undefined, now);
		state.startTail();
		await state.search({ ...filter, text: 'timeout' }, undefined, now);
		await vi.waitFor(() => expect(requests.at(-1)?.text).toBe('timeout'));
		state.destroy();
	});
});

describe('merge', () => {
	it('keeps the newest lines without duplicates', () => {
		const a = line(10, 'a');
		expect(merge([a], [line(10, 'a')])).toHaveLength(1);
		const many = Array.from({ length: MAX_LINES + 50 }, (_, i) => line(i, `l${i}`));
		const merged = merge([], many);
		expect(merged).toHaveLength(MAX_LINES);
		expect(merged[0].message).toBe('l0');
	});
});
