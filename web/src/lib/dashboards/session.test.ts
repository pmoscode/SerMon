import { create } from '@bufbuild/protobuf';
import { timestampMs, type Timestamp } from '@bufbuild/protobuf/wkt';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { api } from '#lib/api/client.js';
import { logsApi } from '#lib/api/logs.js';
import { WidgetKind, WidgetSchema } from '#lib/api/gen/sermon/web/v1/dashboard_pb.js';
import { alertsClient, MAX_LOG_SPAN, sessionSource } from './session.js';

const web1 = '0193f5a0-0000-7000-8000-000000000001';
const db1 = '0193f5a0-0000-7000-8000-000000000003';
const gWeb = '0193f5a0-0000-7000-8000-0000000000aa';
const fleet = [
	{ id: web1, hostname: 'web-01', groupId: gWeb, groupName: 'web' },
	{ id: db1, hostname: 'db-01', groupId: 'g2', groupName: 'db' }
];
const range = { from: Date.UTC(2026, 0, 1), to: Date.UTC(2026, 0, 31) };
const source = (values = {}) => sessionSource({ fleet: () => fleet, values: () => values });

function widget(
	kind: WidgetKind,
	target: { nodeIds?: string[]; groupId?: string; variable?: string },
	extra = {}
) {
	return create(WidgetSchema, {
		id: 'w',
		kind,
		title: 'W',
		query: { metric: 'sermon_cpu_utilization_ratio', target, ...extra }
	});
}

describe('session source', () => {
	afterEach(() => vi.restoreAllMocks());

	it('reads a metric of the nodes of a group with the unit of the catalog and names the hosts', async () => {
		vi.spyOn(api.metrics, 'listMetrics').mockResolvedValue({
			metrics: [{ name: 'sermon_cpu_utilization_ratio', unit: 'ratio_ppm' }]
		} as never);
		const query = vi.spyOn(api.metrics, 'queryMetrics').mockResolvedValue({
			series: [
				{
					nodeId: web1,
					labels: { mode: 'user' },
					points: [{ time: { seconds: 1n, nanos: 0 }, avg: 5, min: 1, max: 9 }]
				}
			]
		} as never);
		const r = await source().load(
			widget(WidgetKind.TIMESERIES, { groupId: gWeb }, { labels: { mode: 'user' } }),
			range
		);
		expect(query.mock.calls[0][0]).toMatchObject({
			selector: { nodeIds: [web1], labels: { mode: 'user' } }
		});
		expect(timestampMs(query.mock.calls[0][0].from as Timestamp)).toBe(range.from);
		expect(r).toEqual({
			kind: 'metrics',
			unit: 'ratio_ppm',
			series: [
				{
					nodeId: web1,
					hostname: 'web-01',
					labels: { mode: 'user' },
					points: [{ time: 1000, value: 5, min: 1, max: 9 }]
				}
			]
		});
	});

	it('asks nothing for a widget without nodes (a variable without a value)', async () => {
		vi.spyOn(api.metrics, 'listMetrics').mockResolvedValue({ metrics: [] } as never);
		const query = vi.spyOn(api.metrics, 'queryMetrics');
		const r = await source({ node: undefined }).load(
			widget(WidgetKind.STAT, { variable: 'node' }),
			range
		);
		expect(r).toEqual({ kind: 'metrics', unit: 'count', series: [] });
		expect(query).not.toHaveBeenCalled();
	});

	it('reads the newest log lines of at most 14 days', async () => {
		const query = vi
			.spyOn(logsApi.logs, 'queryLogs')
			.mockResolvedValue({ lines: [], more: true } as never);
		const r = await source().load(
			widget(
				WidgetKind.LOG_STREAM,
				{ groupId: gWeb },
				{ metric: '', text: 'error', minSeverity: 4 }
			),
			range
		);
		const req = query.mock.calls[0][0];
		expect(
			timestampMs(req.filter!.to as Timestamp) - timestampMs(req.filter!.from as Timestamp)
		).toBe(MAX_LOG_SPAN);
		expect(req).toMatchObject({
			limit: 50,
			filter: { groupId: gWeb, nodeIds: [], text: 'error', minSeverity: 4 }
		});
		expect(r).toEqual({ kind: 'logs', lines: [], more: true });
	});

	it('keeps the alerts of the widget’s nodes, or all without a target', async () => {
		const alerts = [{ key: { nodeId: web1 } }, { key: { nodeId: db1 } }];
		vi.spyOn(await alertsClient(), 'listAlerts').mockResolvedValue({ alerts } as never);
		const some = await source().load(
			widget(WidgetKind.ALERT_LIST, { nodeIds: [db1] }, { metric: '' }),
			range
		);
		expect(some).toEqual({ kind: 'alerts', alerts: [alerts[1]] });
		const all = await source().load(widget(WidgetKind.ALERT_LIST, {}, { metric: '' }), range);
		expect(all).toEqual({ kind: 'alerts', alerts });
	});
});
