import { create } from '@bufbuild/protobuf';
import { Code, ConnectError } from '@connectrpc/connect';
import { describe, expect, it } from 'vitest';
import { DashboardSpecSchema } from '#lib/api/gen/sermon/web/v1/dashboard_pb.js';
import type { KioskClient } from './api.js';
import { KioskState } from './kiosk.svelte.js';

const spec = (title: string) => create(DashboardSpecSchema, { version: 1, title });

/** A fake KioskService per token: answers the dashboard or fails as told. */
function fakes() {
	const answers = new Map<string, () => Promise<unknown>>();
	const client = (token: string) =>
		({
			getKioskDashboard: () => answers.get(token)!(),
			queryKioskWidget: () => answers.get(`${token}:widget`)!()
		}) as unknown as KioskClient;
	const ok =
		(title: string, revision = 1n) =>
		() =>
			Promise.resolve({ spec: spec(title), revision });
	const fail = (code: Code) => () => Promise.reject(new ConnectError('x', code));
	return { answers, client, ok, fail };
}

describe('kiosk state', () => {
	it('rotates among the boards it can show', async () => {
		const f = fakes();
		f.answers.set('a', f.ok('A')).set('b', f.ok('B')).set('c', f.fail(Code.Unauthenticated));
		const k = new KioskState(['a', 'b', 'c'], { client: f.client });
		expect(k.current).toBeUndefined();
		await k.refresh();
		expect(k.shown.map((b) => b.spec?.title)).toEqual(['A', 'B']);
		expect(k.current?.spec?.title).toBe('A');
		k.rotate();
		expect(k.current?.spec?.title).toBe('B');
		k.rotate();
		expect(k.current?.spec?.title).toBe('A');
		// The index stays within the boards (a display runs for months).
		expect(k.index).toBe(0);
		expect(k.allInvalid).toBe(false);
	});

	it('shows nothing of a token that stopped working, also after it showed data', async () => {
		const f = fakes();
		f.answers.set('a', f.ok('A'));
		const k = new KioskState(['a'], { client: f.client });
		await k.refresh();
		expect(k.current?.spec?.title).toBe('A');
		f.answers.set('a', f.fail(Code.Unauthenticated));
		await k.refresh();
		expect(k.current).toBeUndefined();
		expect(k.boards[0].spec).toBeUndefined();
		expect(k.allInvalid).toBe(true);
		// An invalid token is not asked again: the address needs a new token.
		f.answers.set('a', f.ok('A'));
		await k.refresh();
		expect(k.allInvalid).toBe(true);
	});

	it('a widget answering unauthenticated ends the board at once', async () => {
		const f = fakes();
		f.answers.set('a', f.ok('A')).set('a:widget', f.fail(Code.Unauthenticated));
		const k = new KioskState(['a'], { client: f.client });
		await k.refresh();
		await expect(
			k.boards[0].source.load({ id: 'w' } as never, { from: 0, to: 1 })
		).rejects.toThrow();
		expect(k.allInvalid).toBe(true);
	});

	it('keeps the data through an outage, says since when it is stale and reconnects', async () => {
		let now = 1000;
		const f = fakes();
		f.answers.set('a', f.ok('A'));
		const k = new KioskState(['a'], { client: f.client, now: () => now });
		await k.refresh();
		expect(k.current?.staleSince).toBeUndefined();
		f.answers.set('a', f.fail(Code.Unavailable));
		now = 2000;
		await k.refresh();
		now = 3000;
		await k.refresh();
		expect(k.current?.spec?.title).toBe('A');
		expect(k.current?.status).toBe('offline');
		expect(k.current?.staleSince).toBe(2000);
		f.answers.set('a', f.ok('A'));
		await k.refresh();
		expect(k.current?.status).toBe('ready');
		expect(k.current?.staleSince).toBeUndefined();
	});

	it('is stale while a widget fails and current again once it loads', () => {
		const f = fakes();
		const k = new KioskState(['a'], { client: f.client, now: () => 5 });
		k.widgetStatus('a', 'cpu', false);
		k.widgetStatus('a', 'mem', false);
		k.widgetStatus('a', 'cpu', true);
		expect(k.boards[0].staleSince).toBe(5);
		k.widgetStatus('a', 'mem', true);
		expect(k.boards[0].staleSince).toBeUndefined();
	});

	it('takes a changed dashboard and keeps an unchanged one', async () => {
		const f = fakes();
		f.answers.set('a', f.ok('A', 1n));
		const k = new KioskState(['a'], { client: f.client });
		await k.refresh();
		const first = k.boards[0].spec;
		await k.refresh();
		expect(k.boards[0].spec).toBe(first);
		f.answers.set('a', f.ok('A2', 2n));
		await k.refresh();
		expect(k.boards[0].spec?.title).toBe('A2');
	});
});
