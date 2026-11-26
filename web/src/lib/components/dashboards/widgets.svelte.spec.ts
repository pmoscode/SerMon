import { create } from '@bufbuild/protobuf';
import { timestampFromMs } from '@bufbuild/protobuf/wkt';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { AlertSchema, AlertState } from '#lib/api/gen/sermon/web/v1/alert_pb.js';
import { WidgetKind, WidgetSchema } from '#lib/api/gen/sermon/web/v1/dashboard_pb.js';
import { LogLineSchema } from '#lib/api/gen/sermon/web/v1/log_pb.js';
import { LogSeverity } from '#lib/api/gen/sermon/web/v1/log_settings_pb.js';
import type { WidgetResult, WidgetSeries, WidgetSource } from '#lib/dashboards/source.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { timeRange } from '#lib/stores/timerange.svelte.js';
import WidgetView from './WidgetView.svelte';
import AlertListWidget from './widgets/AlertListWidget.svelte';
import GaugeWidget from './widgets/GaugeWidget.svelte';
import LogStreamWidget from './widgets/LogStreamWidget.svelte';
import StatWidget from './widgets/StatWidget.svelte';
import TableWidget from './widgets/TableWidget.svelte';
import TimeseriesWidget from './widgets/TimeseriesWidget.svelte';

const hostile = '<img src=x onerror=alert(1)>';
const now = Date.now();

function series(n: number, values: number[] = [100_000, 500_000], host = 'web'): WidgetSeries[] {
	return Array.from({ length: n }, (_, i) => ({
		nodeId: `n${i}`,
		hostname: `${host}-${String(i).padStart(3, '0')}`,
		labels: { mode: `m${i}` },
		points: values.map((v, j) => ({
			time: now - (values.length - j) * 60_000,
			value: v,
			min: v - 1,
			max: v + 1
		}))
	}));
}

const text = () => document.body.textContent ?? '';
const noInjectedImage = () => expect(document.querySelector('img[src="x"]')).toBeNull();

describe('dashboard widgets', () => {
	beforeEach(() => {
		switchLocale('en');
		timeRange.setPreset('1h');
	});

	describe('time series', () => {
		it('says that there is no data', async () => {
			render(TimeseriesWidget, { title: 'CPU', unit: 'ratio_ppm', series: [], height: 120 });
			await expect.element(page.getByText('No data in the selected time range.')).toBeVisible();
		});

		it('draws at most 40 lines and names the rest', async () => {
			render(TimeseriesWidget, {
				title: 'CPU',
				unit: 'ratio_ppm',
				series: series(100),
				height: 160
			});
			await expect.element(page.getByText('60 more series not shown')).toBeVisible();
			expect(
				page.getByRole('figure', { name: 'CPU' }).element().querySelectorAll('.sr-only li')
			).toHaveLength(40);
		});

		it('survives points without time or value', async () => {
			const broken: WidgetSeries[] = [
				{
					nodeId: 'n',
					hostname: hostile,
					labels: {},
					points: [
						{ time: NaN, value: NaN, min: NaN, max: NaN },
						{ time: now, value: 1, min: 1, max: 1 }
					]
				}
			];
			render(TimeseriesWidget, { title: 'CPU', unit: 'count', series: broken, height: 120 });
			await expect.element(page.getByRole('figure', { name: 'CPU' })).toBeVisible();
			noInjectedImage();
		});
	});

	describe('gauge', () => {
		it('shows a share against 100 %', async () => {
			render(GaugeWidget, { unit: 'ratio_ppm', series: series(1) });
			await expect.element(page.getByRole('img', { name: '50%' })).toBeVisible();
			const arc = document.querySelectorAll('path')[1];
			expect(arc.getAttribute('stroke-dasharray')).toBe('50.0 100');
		});

		it('shows the highest of many series and names it', async () => {
			const many = series(500);
			many[321].points.at(-1)!.value = 900_000;
			render(GaugeWidget, { unit: 'ratio_ppm', series: many });
			await expect.element(page.getByRole('img', { name: '90%' })).toBeVisible();
			expect(text()).toContain('web-321 m321');
		});

		it('has no data without finite values', async () => {
			render(GaugeWidget, { unit: 'bytes', series: series(2, [NaN, NaN]) });
			await expect.element(page.getByText(/No data in this range/)).toBeVisible();
			render(GaugeWidget, { unit: 'bytes', series: [] });
			expect(page.getByText(/No data in this range/).elements()).toHaveLength(2);
		});
	});

	describe('stat', () => {
		it('shows the latest value of one series', async () => {
			render(StatWidget, { unit: 'ratio_ppm', series: series(1, [NaN, 250_000, NaN]) });
			await expect.element(page.getByText('25%')).toBeVisible();
		});

		it('lists at most four series and names the rest', async () => {
			render(StatWidget, { unit: 'count', series: series(10, [3]) });
			await expect.element(page.getByText('6 more series not shown')).toBeVisible();
			expect(page.getByRole('listitem').elements()).toHaveLength(5);
		});

		it('has no data for an empty answer', async () => {
			render(StatWidget, { unit: 'count', series: [] });
			await expect.element(page.getByText(/No data in this range/)).toBeVisible();
		});
	});

	describe('table', () => {
		it('lists latest, minimum and maximum per series, at most 200 rows', async () => {
			render(TableWidget, {
				title: 'Load',
				unit: 'count',
				series: series(500, [1, 5]),
				labels: {}
			});
			const table = page.getByRole('table', { name: 'Load' });
			await expect.element(table).toBeVisible();
			expect(table.element().querySelectorAll('tbody tr')).toHaveLength(200);
			await expect.element(page.getByText('300 more rows not shown')).toBeVisible();
			const first = table.element().querySelector('tbody tr')!.textContent ?? '';
			expect(first).toContain('web-000');
			expect(first).toContain('mode=m0');
		});

		it('shows a dash for missing values and host names as text', async () => {
			const broken: WidgetSeries[] = [{ nodeId: 'n', hostname: hostile, labels: {}, points: [] }];
			render(TableWidget, { title: 'Load', unit: 'count', series: broken });
			await expect.element(page.getByRole('cell', { name: hostile })).toBeVisible();
			expect(page.getByRole('cell', { name: '–' }).elements()).toHaveLength(3);
			noInjectedImage();
		});

		it('has no data for an empty answer', async () => {
			render(TableWidget, { title: 'Load', unit: 'count', series: [] });
			await expect.element(page.getByText(/No data in this range/)).toBeVisible();
		});
	});

	describe('log stream', () => {
		const line = (i: number, change = {}) =>
			create(LogLineSchema, {
				nodeId: 'n',
				hostname: 'web-01',
				at: timestampFromMs(now - i * 1000),
				severity: LogSeverity.ERROR,
				message: `line ${i}`,
				...change
			});

		it('shows at most 50 lines and points to the explorer', async () => {
			render(LogStreamWidget, {
				title: 'Logs',
				lines: Array.from({ length: 500 }, (_, i) => line(i))
			});
			const list = page.getByRole('list', { name: 'Logs' });
			await expect.element(list).toBeVisible();
			expect(list.element().querySelectorAll('li')).toHaveLength(50);
			await expect.element(page.getByText('More lines in the log explorer')).toBeVisible();
		});

		it('keeps hostile lines as text and survives lines without time', async () => {
			render(LogStreamWidget, {
				title: 'Logs',
				lines: [line(0, { at: undefined, message: hostile, hostname: hostile })]
			});
			await expect.element(page.getByText('–', { exact: true })).toBeVisible();
			expect(text()).toContain(hostile);
			noInjectedImage();
		});

		it('says that there are no lines', async () => {
			render(LogStreamWidget, { title: 'Logs', lines: [] });
			await expect.element(page.getByText('No log lines in this range.')).toBeVisible();
		});
	});

	describe('alert list', () => {
		const alert = (i: number, state: AlertState, hostname = `web-${i}`) =>
			create(AlertSchema, {
				key: { ruleId: 'system.cpu.usage_high', nodeId: `n${i}` },
				state,
				hostname,
				ruleName: { en: 'CPU usage high', de: 'CPU-Last hoch' }
			});

		it('lists the firing alerts, the most severe first, at most 100', async () => {
			const alerts = [
				alert(1, AlertState.WARNING),
				alert(2, AlertState.RESOLVED),
				alert(3, AlertState.CRITICAL),
				...Array.from({ length: 300 }, (_, i) => alert(10 + i, AlertState.PENDING))
			];
			render(AlertListWidget, { title: 'Alerts', alerts });
			const list = page.getByRole('list', { name: 'Alerts' });
			await expect.element(list).toBeVisible();
			const items = list.element().querySelectorAll(':scope > li');
			expect(items).toHaveLength(100);
			expect(items[0].textContent).toContain('web-3');
			expect(items[1].textContent).toContain('web-1');
			expect(list.element().textContent).not.toContain('web-2');
			await expect.element(page.getByText('202 more alerts not shown')).toBeVisible();
		});

		it('shows names from the host as text and an empty state', async () => {
			render(AlertListWidget, {
				title: 'Alerts',
				alerts: [alert(1, AlertState.CRITICAL, hostile)]
			});
			await expect.element(page.getByText(hostile)).toBeVisible();
			noInjectedImage();
			render(AlertListWidget, { title: 'None', alerts: [alert(2, AlertState.OK)] });
			await expect.element(page.getByText('No active alerts.')).toBeVisible();
		});
	});

	describe('widget view', () => {
		const widget = create(WidgetSchema, {
			id: 'cpu',
			kind: WidgetKind.STAT,
			title: 'CPU',
			query: {}
		});

		it('says when the data could not be loaded', async () => {
			const onstatus = vi.fn();
			const source: WidgetSource = { load: () => Promise.reject(new Error('down')) };
			render(WidgetView, { widget, source, range: timeRange.range, height: 100, onstatus });
			await expect
				.element(page.getByRole('alert'))
				.toHaveTextContent('The data could not be loaded.');
			expect(onstatus).toHaveBeenCalledWith(false);
		});

		it('keeps the last data when a refresh fails and marks it as stale', async () => {
			const results: (WidgetResult | Error)[] = [
				{ kind: 'metrics', unit: 'count', series: series(1, [7]) },
				new Error('down')
			];
			const source: WidgetSource = {
				load: () => {
					const next = results.shift()!;
					return next instanceof Error ? Promise.reject(next) : Promise.resolve(next);
				}
			};
			const onstatus = vi.fn();
			const { rerender } = await render(WidgetView, {
				widget,
				source,
				range: { from: now - 1000, to: now },
				height: 100,
				onstatus
			});
			await expect.element(page.getByText('7', { exact: true })).toBeVisible();
			await rerender({ range: { from: now - 500, to: now + 500 } });
			await expect
				.element(page.getByRole('alert'))
				.toHaveTextContent('Not updated: the last refresh failed.');
			await expect.element(page.getByText('7', { exact: true })).toBeVisible();
			expect(onstatus.mock.calls).toEqual([[true], [false]]);
		});
	});
});
