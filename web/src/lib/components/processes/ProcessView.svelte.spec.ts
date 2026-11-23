import { create, type MessageInitShape } from '@bufbuild/protobuf';
import { timestampFromMs } from '@bufbuild/protobuf/wkt';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page, userEvent } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import {
	ProcessInfoSchema,
	WatchProcessesResponseSchema
} from '#lib/api/gen/sermon/web/v1/live_view_pb.js';
import { liveViewApi } from '#lib/api/liveview.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import ProcessView from './ProcessView.svelte';

type Response = MessageInitShape<typeof WatchProcessesResponseSchema>;
type Info = MessageInitShape<typeof ProcessInfoSchema>;

const node = '0193f5a0-0000-7000-8000-000000000001';
const hostile = '<img src=x onerror=alert(1)>\u202Egpj.exe';

const processes: Info[] = [
	{
		pid: 1,
		ppid: 0,
		uid: 0,
		user: 'root',
		state: 'S',
		name: 'systemd',
		threads: 1,
		cpuRatio: 0.01,
		rssBytes: 12_000_000n,
		memoryRatio: 0.003,
		runtimeSeconds: 86_400n,
		cmdline: '/sbin/init splash',
		unit: 'init.scope'
	},
	{
		pid: 812,
		ppid: 1,
		uid: 33,
		user: 'www-data',
		state: 'R',
		name: 'nginx',
		threads: 4,
		cpuRatio: 0.42,
		rssBytes: 48_000_000n,
		memoryRatio: 0.012,
		runtimeSeconds: 3_600n,
		cmdline: 'nginx: worker process',
		unit: 'nginx.service'
	},
	{
		pid: 900,
		ppid: 812,
		uid: 33,
		user: 'www-data',
		state: 'S',
		name: hostile,
		threads: 1,
		cpuRatio: 0.2,
		cmdline: `${hostile} --password=***`,
		cmdlineTruncated: true,
		container: '0123456789ab'
	},
	{
		pid: 2,
		ppid: 0,
		uid: 0,
		user: 'root',
		state: 'S',
		name: 'kthreadd',
		threads: 1,
		cpuRatio: 0,
		kernelThread: true,
		cmdline: ''
	},
	{
		pid: 50,
		ppid: 2,
		uid: 0,
		user: 'root',
		state: 'I',
		name: 'kworker/0:1',
		threads: 1,
		cpuRatio: 0.05,
		kernelThread: true,
		cmdline: ''
	}
];

function snapshot(list: Info[] = processes, extra: Record<string, unknown> = {}): Response {
	return {
		event: {
			case: 'snapshot',
			value: {
				takenAt: timestampFromMs(Date.now()),
				summary: {
					cpuBusyRatio: 0.25,
					cpuCount: 4,
					memoryTotalBytes: 8n * 1024n ** 3n,
					memoryAvailableBytes: 6n * 1024n ** 3n,
					swapTotalBytes: 0n,
					load1: 0.5,
					load5: 0.4,
					load15: 0.3,
					uptimeSeconds: 90_000n,
					tasks: { total: 5, running: 1, sleeping: 4 }
				},
				processes: list,
				...extra
			}
		}
	};
}

/** A stream the test feeds message by message. */
function stream() {
	let push: (r: Response | null) => void = () => {};
	const queue: Response[] = [];
	const spy = vi.spyOn(liveViewApi.liveView, 'watchProcesses').mockImplementation(((
		_r: unknown,
		options?: { signal?: AbortSignal }
	) =>
		(async function* () {
			while (!options?.signal?.aborted) {
				const next =
					queue.shift() ??
					(await new Promise<Response | null>((done) => {
						push = done;
						options?.signal?.addEventListener('abort', () => done(null));
					}));
				if (next === null) return;
				yield create(WatchProcessesResponseSchema, next);
			}
		})()) as never);
	return {
		spy,
		send(r: Response) {
			queue.push(r);
			const p = push;
			push = () => {};
			p(queue.shift()!);
		}
	};
}

const table = () => page.getByRole('region', { name: 'Processes' });
const names = () =>
	[
		...table().element().querySelectorAll('tbody tr:not([aria-hidden]) td:nth-child(10) button')
	].map((b) => b.textContent);

describe('ProcessView', () => {
	beforeEach(() => switchLocale('en'));
	afterEach(() => vi.restoreAllMocks());

	it('shows the header and the busiest processes first, host texts as text', async () => {
		const s = stream();
		render(ProcessView, { nodeId: node });
		await expect.element(page.getByText('Connecting to the agent …')).toBeVisible();
		expect(s.spy.mock.calls[0][0]).toEqual({ nodeId: node });
		s.send(snapshot());
		await expect.element(page.getByText('Live, updated every 2 seconds')).toBeVisible();
		await expect.element(page.getByText('2 GiB of 8 GiB')).toBeVisible();
		await expect.element(page.getByText('0.50 · 0.40 · 0.30')).toBeVisible();
		await expect.element(page.getByText('5 of 5 processes')).toBeVisible();
		expect(names()).toEqual([
			'nginx',
			'<img src=x onerror=alert(1)>⟨U+202E⟩gpj.exe',
			'[kworker/0:1]',
			'systemd',
			'[kthreadd]'
		]);
		expect(document.querySelector('img[src="x"]')).toBeNull();
		expect(table().element().textContent).not.toContain('\u202E');
		await expect
			.element(page.getByRole('columnheader', { name: 'CPU' }))
			.toHaveAttribute('aria-sort', 'descending');
	});

	it('sorts by a column and turns it around', async () => {
		const s = stream();
		render(ProcessView, { nodeId: node });
		s.send(snapshot());
		await expect.element(table()).toBeVisible();
		await page.getByRole('button', { name: 'PID', exact: true }).click();
		await vi.waitFor(() => expect(names()[0]).toBe('systemd'));
		expect(names().at(-1)).toContain('gpj.exe');
		await expect
			.element(page.getByRole('columnheader', { name: 'PID', exact: true }))
			.toHaveAttribute('aria-sort', 'ascending');
		await page.getByRole('button', { name: 'PID', exact: true }).click();
		await vi.waitFor(() => expect(names()[0]).toContain('gpj.exe'));
	});

	it('filters by user, name and regex and names an invalid regex', async () => {
		const s = stream();
		render(ProcessView, { nodeId: node });
		s.send(snapshot());
		await page.getByLabelText('User').selectOptions('www-data');
		await vi.waitFor(() => expect(names()).toHaveLength(2));
		await expect.element(page.getByText('2 of 5 processes')).toBeVisible();
		await page.getByLabelText('User').selectOptions('');
		await page.getByLabelText('Name').fill('KWORK');
		await vi.waitFor(() => expect(names()).toEqual(['[kworker/0:1]']));
		await page.getByLabelText('Name').fill('');
		await page.getByLabelText('Regex').fill('worker\\s+process$');
		await vi.waitFor(() => expect(names()).toEqual(['nginx']));
		await page.getByLabelText('Regex').fill('nginx(');
		await expect.element(page.getByRole('alert')).toMatchTextContent(/Invalid regular expression/);
		await expect.element(page.getByLabelText('Regex')).toHaveAttribute('aria-invalid', 'true');
		await vi.waitFor(() => expect(names()).toHaveLength(5));
		await page.getByLabelText('Regex').fill('^nothing$');
		await expect.element(page.getByText('No process matches the filter.')).toBeVisible();
	});

	it('shows the tree from the parents, with the parents of a match as context', async () => {
		const s = stream();
		render(ProcessView, { nodeId: node });
		s.send(snapshot());
		await page.getByLabelText('Tree view').click();
		await vi.waitFor(() =>
			expect(names()).toEqual([
				'systemd',
				'nginx',
				'<img src=x onerror=alert(1)>⟨U+202E⟩gpj.exe',
				'[kthreadd]',
				'[kworker/0:1]'
			])
		);
		await page.getByLabelText('Name').fill('gpj');
		await vi.waitFor(() => expect(names()).toHaveLength(3));
		await expect.element(page.getByText('1 of 5 processes')).toBeVisible();
		expect(table().element().textContent).toContain('parent of a match');
	});

	it('pauses on the shown state and shows the newest on resume', async () => {
		const s = stream();
		render(ProcessView, { nodeId: node });
		s.send(snapshot());
		await expect.element(table()).toBeVisible();
		await page.getByRole('button', { name: 'Pause' }).click();
		s.send(snapshot([{ pid: 7, ppid: 1, name: 'fresh', state: 'R', threads: 1 }]));
		await expect.element(page.getByText(/newer data is waiting/)).toBeVisible();
		expect(names()).toHaveLength(5);
		await page.getByRole('button', { name: 'Resume' }).click();
		await vi.waitFor(() => expect(names()).toEqual(['fresh']));
	});

	it('shows the details with the whole command line, n/a and a process that ended', async () => {
		const s = stream();
		render(ProcessView, { nodeId: node });
		s.send(snapshot());
		await page.getByRole('button', { name: /Details of .*gpj\.exe, PID 900/ }).click();
		const drawer = page.getByRole('dialog');
		await expect.element(drawer).toBeVisible();
		await expect.element(drawer.getByText(/^Read only: SerMon/)).toBeVisible();
		const cmdline = drawer.element().querySelector('pre')!;
		expect(cmdline.textContent).toBe('<img src=x onerror=alert(1)>⟨U+202E⟩gpj.exe --password=***');
		await expect.element(drawer.getByText('Cut to 1 KiB.', { exact: false })).toBeVisible();
		await expect.element(drawer.getByText('nginx, PID 812')).toBeVisible();
		await expect.element(drawer.getByText('www-data (UID 33)')).toBeVisible();
		await expect.element(drawer.getByText('0123456789ab')).toBeVisible();
		// RSS and runtime were not readable.
		expect(drawer.getByText('n/a').elements()).toHaveLength(2);
		expect(document.querySelector('img[src="x"]')).toBeNull();

		s.send(snapshot(processes.filter((p) => p.pid !== 900)));
		await expect.element(drawer.getByText(/no longer running/)).toBeVisible();
		await userEvent.keyboard('{Escape}');
		await expect.element(drawer).not.toBeInTheDocument();
	});

	it('marks hidden command lines and kernel threads', async () => {
		const s = stream();
		render(ProcessView, { nodeId: node });
		s.send(
			snapshot([
				{
					pid: 812,
					ppid: 1,
					user: 'www-data',
					name: 'nginx',
					state: 'S',
					threads: 1,
					cmdlineHidden: true
				}
			])
		);
		await expect.element(table().getByText('hidden')).toBeVisible();
		await page.getByRole('button', { name: /Details of nginx/ }).click();
		await expect
			.element(page.getByRole('dialog').getByText('The organization hides command lines.'))
			.toBeVisible();
	});

	it('says when the agent is offline and shows its refusal as text', async () => {
		const s = stream();
		render(ProcessView, { nodeId: node });
		s.send({ event: { case: 'offline', value: true } });
		await expect.element(page.getByText(/not connected to this server instance/)).toBeVisible();
		s.send({ event: { case: 'refused', value: '<b>allow_live_view = false</b>' } });
		await expect
			.element(page.getByRole('alert'))
			.toMatchTextContent(/The agent refuses the live view: <b>allow_live_view = false<\/b>/);
		expect(document.querySelector('[role="alert"] b')).toBeNull();
		await expect.element(page.getByRole('button', { name: 'Try again' })).toBeVisible();
	});

	it('closes the stream when it goes', async () => {
		const s = stream();
		const view = await render(ProcessView, { nodeId: node });
		s.send(snapshot());
		await expect.element(table()).toBeVisible();
		const signal = (s.spy.mock.calls[0][1] as { signal: AbortSignal }).signal;
		expect(signal.aborted).toBe(false);
		view.unmount();
		expect(signal.aborted).toBe(true);
	});
});
