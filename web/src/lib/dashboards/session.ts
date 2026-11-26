/**
 * The widgets' data in the UI: the existing readers with the viewer's session (ADR 0040) – the
 * server checks every node against the viewer's rights, nodes out of scope stay empty.
 */
import { timestampFromMs } from '@bufbuild/protobuf/wkt';
import type { Client } from '@connectrpc/connect';
import { api, client } from '#lib/api/client.js';
import type { AlertService } from '#lib/api/gen/sermon/web/v1/alert_pb.js';
import { WidgetKind, type Widget } from '#lib/api/gen/sermon/web/v1/dashboard_pb.js';
import type { Unit } from '#lib/format/format.js';
import type { Range } from '#lib/stores/timerange.svelte.js';
import { isMetricKind } from './model.js';
import { seriesOf } from './series.js';
import { unitOf, type WidgetResult, type WidgetSource } from './source.js';
import { targetOf, type FleetNode, type VariableValues } from './variables.js';

/** Log queries span at most 14 days (LogFilter). */
export const MAX_LOG_SPAN = 14 * 86_400_000;
/** Lines of a log stream. */
export const LOG_LINES = 50;
/** QueryMetrics takes at most 100 nodes. */
const MAX_QUERY_NODES = 100;

// Only the alert service: the alerting client would bring the descriptors of rules, channels
// and policies (JavaScript budget).
let alertClient: Promise<Client<typeof AlertService>> | undefined;
export function alertsClient(): Promise<Client<typeof AlertService>> {
	alertClient ??= import('#lib/api/gen/sermon/web/v1/alert_pb.js').then((pb) =>
		client(pb.AlertService)
	);
	return alertClient;
}

export function sessionSource(deps: {
	fleet: () => FleetNode[];
	values: () => VariableValues;
}): WidgetSource {
	let units: Promise<Map<string, Unit>> | undefined;
	const unit = async (metric: string) => {
		units ??= api.metrics
			.listMetrics({})
			.then((res) => new Map(res.metrics.map((d) => [d.name, unitOf(d.unit)])));
		try {
			return (await units).get(metric) ?? 'count';
		} catch {
			units = undefined;
			return 'count';
		}
	};
	return {
		async load(w: Widget, range: Range): Promise<WidgetResult> {
			const fleet = deps.fleet();
			const target = targetOf(w, deps.values(), fleet);
			const q = w.query;
			if (isMetricKind(w.kind)) {
				const u = await unit(q?.metric ?? '');
				if (target.nodeIds.length === 0) return { kind: 'metrics', unit: u, series: [] };
				const res = await api.metrics.queryMetrics({
					selector: {
						metric: q?.metric ?? '',
						nodeIds: target.nodeIds.slice(0, MAX_QUERY_NODES),
						labels: q?.labels ?? {}
					},
					from: timestampFromMs(range.from),
					to: timestampFromMs(range.to)
				});
				return { kind: 'metrics', unit: u, series: seriesOf(res.series, fleet) };
			}
			if (w.kind === WidgetKind.LOG_STREAM) {
				if (target.nodeIds.length === 0 && !target.groupId)
					return { kind: 'logs', lines: [], more: false };
				const { logsApi } = await import('#lib/api/logs.js');
				const res = await logsApi.logs.queryLogs({
					filter: {
						from: timestampFromMs(Math.max(range.from, range.to - MAX_LOG_SPAN)),
						to: timestampFromMs(range.to),
						nodeIds: target.groupId ? [] : target.nodeIds.slice(0, MAX_QUERY_NODES),
						groupId: target.groupId,
						text: q?.text ?? '',
						minSeverity: q?.minSeverity ?? 0
					},
					limit: LOG_LINES
				});
				return { kind: 'logs', lines: res.lines, more: res.more };
			}
			const res = await (await alertsClient()).listAlerts({});
			const nodes = new Set(target.nodeIds);
			return {
				kind: 'alerts',
				alerts: target.all ? res.alerts : res.alerts.filter((a) => a.key && nodes.has(a.key.nodeId))
			};
		}
	};
}
