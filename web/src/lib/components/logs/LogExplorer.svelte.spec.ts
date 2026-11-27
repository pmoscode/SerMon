import { create, type MessageInitShape } from '@bufbuild/protobuf';
import { durationFromMs, timestampFromMs } from '@bufbuild/protobuf/wkt';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page, userEvent } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { api, Code } from '#lib/api/client.js';
import {
	GetFleetSummaryResponseSchema,
	NodeSummarySchema
} from '#lib/api/gen/sermon/web/v1/fleet_pb.js';
import {
	GetLogContextResponseSchema,
	GetLogHistogramResponseSchema,
	LogLineSchema,
	LogSource,
	QueryLogsResponseSchema,
	TailLogsResponseSchema,
	type LogLine
} from '#lib/api/gen/sermon/web/v1/log_pb.js';
import { LogSeverity } from '#lib/api/gen/sermon/web/v1/log_settings_pb.js';
import { QueryMetricsResponseSchema } from '#lib/api/gen/sermon/web/v1/metrics_pb.js';
import { logsApi } from '#lib/api/logs.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { emptyFilter, type LogFilter } from '#lib/logs/filter.js';
import { rpcError } from '#lib/test/fakes.js';
import LogExplorer from './LogExplorer.svelte';

const web01 = '0193f5a0-0000-7000-8000-000000000001';
const db01 = '0193f5a0-0000-7000-8000-000000000002';
const now = Date.now();
const hostile = '<img src=x onerror=alert(1)><script>alert(2)</script> \u202Etxt.exe\x1b[31m red';

function line(
	msAgo: number,
	message: string,
	extra: MessageInitShape<typeof LogLineSchema> = {}
): LogLine {
	return create(LogLineSchema, {
		nodeId: web01,
		hostname: 'web-01',
		at: timestampFromMs(now - msAgo),
		severity: LogSeverity.ERROR,
		source: LogSource.JOURNAL,
		unit: 'nginx.service',
		message,
		fields: { pid: '812' },
		...extra
	});
}

function stubFleet(vetoed = false) {
	return vi.spyOn(api.fleet, 'getFleetSummary').mockResolvedValue(
		create(GetFleetSummaryResponseSchema, {
			nodes: [
				create(NodeSummarySchema, {
					nodeId: web01,
					hostname: 'web-01',
					groupId: 'g1',
					groupName: 'web'
				}),
				create(NodeSummarySchema, {
					nodeId: db01,
					hostname: 'db-01',
					groupId: 'g2',
					groupName: 'db',
					logsVetoed: vetoed
				})
			]
		})
	);
}

function stubMetrics(lost = false) {
	return vi.spyOn(api.metrics, 'queryMetrics').mockResolvedValue(
		create(QueryMetricsResponseSchema, {
			series: lost
				? [
						{
							nodeId: web01,
							points: [
								{ time: timestampFromMs(now - 600_000), max: 2 },
								{ time: timestampFromMs(now - 300_000), max: 9 }
							]
						}
					]
				: []
		})
	);
}

function stubQuery(lines: LogLine[], more = false) {
	return vi
		.spyOn(logsApi.logs, 'queryLogs')
		.mockResolvedValue(create(QueryLogsResponseSchema, { lines, more }));
}

function stubHistogram() {
	return vi.spyOn(logsApi.logs, 'getLogHistogram').mockResolvedValue(
		create(GetLogHistogramResponseSchema, {
			step: durationFromMs(36_000),
			series: [
				{ severity: LogSeverity.ERROR, points: [{ at: timestampFromMs(now - 60_000), count: 3 }] },
				{ severity: LogSeverity.INFO, points: [{ at: timestampFromMs(now - 30_000), count: 5 }] }
			]
		})
	);
}

function show(filter: LogFilter = emptyFilter(), fixedNode?: string) {
	const onFilter = vi.fn();
	const view = render(LogExplorer, { filter, onFilter, fixedNode });
	return { onFilter, view };
}

describe('LogExplorer', () => {
	beforeEach(() => switchLocale('en'));
	afterEach(() => vi.restoreAllMocks());

	it('shows a hostile line as text only, with bidi and control characters visible', async () => {
		stubFleet();
		stubMetrics();
		stubQuery([line(1000, hostile, { unit: '\u202Eecivres.dhss' })]);
		stubHistogram();
		show();
		// M3-28: the unit comes from the host as well (a syslog identifier anyone can set).
		await expect.element(page.getByText('⟨U+202E⟩ecivres.dhss')).toBeVisible();
		const row = page.getByText('txt.exe', { exact: false });
		await expect.element(row).toBeVisible();
		const text = row.element().closest('p')!;
		expect(text.textContent).toBe(
			'<img src=x onerror=alert(1)><script>alert(2)</script> ⟨U+202E⟩txt.exe red'
		);
		expect(document.querySelector('img[src="x"]')).toBeNull();
		expect(text.querySelector('script')).toBeNull();
		expect(text.textContent).not.toContain('\u202E');
		expect(text.textContent).not.toContain('\x1b');
	});

	it('shows lines with severity, host, unit and the histogram totals', async () => {
		stubFleet();
		stubMetrics();
		stubQuery(
			[line(1000, 'upstream timed out'), line(2000, 'started', { severity: LogSeverity.INFO })],
			true
		);
		stubHistogram();
		show();
		await expect.element(page.getByText('upstream timed out')).toBeVisible();
		await expect.element(page.getByText('2 lines, there are more')).toBeVisible();
		const lines = page.getByRole('region', { name: 'Lines' });
		await expect.element(lines.getByText('web-01').first()).toBeVisible();
		await expect.element(lines.getByText('nginx.service').first()).toBeVisible();
		const figure = page.getByRole('figure');
		await expect.element(figure).toMatchTextContent(/Error and worse\s*3/);
		await expect.element(figure).toMatchTextContent(/Notice and info\s*5/);
		await expect.element(page.getByRole('button', { name: 'Load older lines' })).toBeVisible();
	});

	it('applies the filters as one filter', async () => {
		stubFleet();
		stubMetrics();
		stubQuery([]);
		stubHistogram();
		const { onFilter } = show();
		await expect.element(page.getByLabelText('Node')).toBeVisible();
		await page.getByLabelText('Node').selectOptions('db-01');
		await page.getByLabelText('Severity').selectOptions('Warning and above');
		await page.getByLabelText('Source').selectOptions('File');
		await page.getByLabelText('Units').fill('nginx.service, cron ,, nginx.service');
		await page.getByLabelText('Text').fill('timed out');
		await page.getByLabelText('Ignore case').click();
		await page.getByRole('button', { name: 'Search' }).click();
		expect(onFilter).toHaveBeenLastCalledWith({
			...emptyFilter(),
			nodes: [db01],
			sources: ['file'],
			units: ['nginx.service', 'cron'],
			severity: 'warning',
			text: 'timed out',
			ignoreCase: true
		});
		await page.getByRole('button', { name: '24 h' }).click();
		expect(onFilter.mock.lastCall![0].preset).toBe('24h');
	});

	it('shows the empty, error and no-node states', async () => {
		stubFleet();
		stubMetrics();
		stubHistogram();
		const query = stubQuery([]);
		show();
		await expect.element(page.getByText('No lines for this filter.')).toBeVisible();
		query.mockRejectedValue(rpcError(Code.Unavailable));
		await page.getByRole('button', { name: 'Refresh' }).click();
		await expect.element(page.getByRole('alert')).toMatchTextContent(/log store \(Loki\)/);
	});

	it('names nodes that veto logs and nodes that lost journal entries', async () => {
		stubFleet(true);
		stubMetrics(true);
		stubQuery([]);
		stubHistogram();
		show();
		await expect.element(page.getByText(/allow_logs = false\): db-01/)).toBeVisible();
		await expect.element(page.getByText(/lost journal entries .*web-01 \(7\)/)).toBeVisible();
	});

	it('opens the context of a line with the entry marked', async () => {
		stubFleet();
		stubMetrics();
		const entry = line(1000, 'the entry');
		stubQuery([entry]);
		stubHistogram();
		const context = vi.spyOn(logsApi.logs, 'getLogContext').mockResolvedValue(
			create(GetLogContextResponseSchema, {
				lines: [line(3000, 'before'), entry, line(500, 'after')]
			})
		);
		show();
		await page.getByRole('button', { name: /Details of the line/ }).click();
		await expect.element(page.getByText('812')).toBeVisible();
		await page.getByRole('button', { name: 'Lines before and after' }).click();
		const dialog = page.getByRole('dialog');
		await expect.element(dialog.getByText('after')).toBeVisible();
		expect(context.mock.calls[0][0]).toMatchObject({ nodeId: web01, unit: 'nginx.service' });
		const current = dialog.element().querySelector('[aria-current="true"]');
		expect(current?.textContent).toContain('the entry');
	});

	it('tails new lines on top and holds them while paused', async () => {
		stubFleet();
		stubMetrics();
		stubQuery([line(60_000, 'old')]);
		stubHistogram();
		let push: (lines: LogLine[]) => void = () => {};
		vi.spyOn(logsApi.logs, 'tailLogs').mockImplementation(((
			_r: unknown,
			options?: { signal?: AbortSignal }
		) =>
			(async function* () {
				yield create(TailLogsResponseSchema, {});
				while (!options?.signal?.aborted) {
					const lines = await new Promise<LogLine[]>((done) => (push = done));
					yield create(TailLogsResponseSchema, { lines });
				}
			})()) as never);
		show();
		await expect.element(page.getByText('old')).toBeVisible();
		await page.getByRole('button', { name: 'Live' }).click();
		await expect.element(page.getByText('Live: new lines appear at the top.')).toBeVisible();
		push([line(100, 'fresh')]);
		await expect.element(page.getByText('fresh')).toBeVisible();
		await page.getByRole('button', { name: 'Pause' }).click();
		push([line(50, 'waiting')]);
		await expect.element(page.getByText('Paused: 1 new lines waiting.')).toBeVisible();
		expect(page.getByText('waiting', { exact: true }).elements()).toHaveLength(0);
		await page.getByRole('button', { name: 'Resume' }).click();
		await expect.element(page.getByText('waiting', { exact: true })).toBeVisible();
		await page.getByRole('button', { name: 'Stop live' }).click();
		await expect.element(page.getByRole('button', { name: 'Live' })).toBeVisible();
	});

	it('zooms to a dragged part of the histogram', async () => {
		stubFleet();
		stubMetrics();
		stubQuery([]);
		stubHistogram();
		const { onFilter } = show({ ...emptyFilter(), from: now - 100_000, to: now });
		const svg = page.getByTestId('log-histogram');
		await expect.element(svg).toBeVisible();
		const area = svg.element().parentElement!;
		const box = area.getBoundingClientRect();
		const at = (share: number) => ({
			clientX: box.left + box.width * share,
			clientY: box.top + 10,
			button: 0,
			pointerId: 1,
			bubbles: true
		});
		area.dispatchEvent(new PointerEvent('pointerdown', at(0.25)));
		area.dispatchEvent(new PointerEvent('pointermove', at(0.75)));
		area.dispatchEvent(new PointerEvent('pointerup', at(0.75)));
		const zoomed = onFilter.mock.lastCall![0] as LogFilter;
		expect(zoomed.from).toBeCloseTo(now - 75_000, -2);
		expect(zoomed.to).toBeCloseTo(now - 25_000, -2);
		// A click is no zoom.
		onFilter.mockClear();
		area.dispatchEvent(new PointerEvent('pointerdown', at(0.5)));
		area.dispatchEvent(new PointerEvent('pointerup', at(0.5)));
		expect(onFilter).not.toHaveBeenCalled();
		void userEvent;
	});

	// V1-24: over 14 days the counts answer in steps of 4 h at whole hours: the first step reaches
	// before the range, the last one past now; every count lands in a bar.
	it('shows the counts of a long range in aligned steps', async () => {
		stubFleet();
		stubMetrics();
		stubQuery([]);
		const hour = 3_600_000;
		const from = now - 14 * 24 * hour;
		const first = Math.ceil(from / (4 * hour)) * 4 * hour;
		const last = Math.ceil(now / (4 * hour)) * 4 * hour;
		vi.spyOn(logsApi.logs, 'getLogHistogram').mockResolvedValue(
			create(GetLogHistogramResponseSchema, {
				step: durationFromMs(4 * hour),
				series: [
					{
						severity: LogSeverity.ERROR,
						points: [
							{ at: timestampFromMs(first), count: 2 },
							{ at: timestampFromMs(last), count: 7 }
						]
					},
					{
						severity: LogSeverity.WARNING,
						points: [{ at: timestampFromMs(first + 8 * hour), count: 1 }]
					}
				]
			})
		);
		show({ ...emptyFilter(), preset: '14d' });
		const figure = page.getByRole('figure');
		await expect.element(figure).toMatchTextContent(/Error and worse\s*9/);
		await expect.element(figure).toMatchTextContent(/Warning\s*1/);
		const bars = page.getByTestId('log-histogram').element().querySelectorAll('rect');
		expect(bars).toHaveLength(3);
		const xs = [...bars].map((r) => Number(r.getAttribute('x')));
		expect(Math.min(...xs)).toBeLessThan(10);
		expect(Math.max(...xs)).toBeGreaterThan(980);
	});

	// A text or units over a long range: Loki counts every line and gives up; the histogram asks
	// for a shorter range instead of failing without a reason.
	it('asks for a shorter range when the histogram is too broad', async () => {
		stubFleet();
		stubMetrics();
		stubQuery([]);
		vi.spyOn(logsApi.logs, 'getLogHistogram').mockRejectedValue(rpcError(Code.FailedPrecondition));
		show({ ...emptyFilter(), preset: '14d', text: 'timed out' });
		await expect
			.element(
				page
					.getByRole('figure')
					.getByText(/shorten the range/)
					.first()
			)
			.toBeVisible();
		expect(
			page
				.getByRole('status')
				.getByText(/shorten the range/)
				.elements()
		).toHaveLength(1);
		expect(page.getByText('The histogram could not be loaded.').elements()).toHaveLength(0);
	});

	it('hides the node and group choice on a node page and asks for that node only', async () => {
		stubFleet();
		stubMetrics();
		const query = stubQuery([line(1000, 'x')]);
		stubHistogram();
		show({ ...emptyFilter(), nodes: [db01] }, web01);
		await expect.element(page.getByText('x', { exact: true })).toBeVisible();
		expect(page.getByLabelText('Node').elements()).toHaveLength(0);
		expect(query.mock.calls[0][0].filter!.nodeIds).toEqual([web01]);
		// The host is the page's: not repeated in every line.
		expect(page.getByText('web-01').elements()).toHaveLength(0);
	});
});
