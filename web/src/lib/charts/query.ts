import { timestampFromMs, timestampMs } from '@bufbuild/protobuf/wkt';
import { api } from '#lib/api/client.js';
import type { Range } from '#lib/stores/timerange.svelte.js';
import type { ChartSeries } from './data.js';

/** One metric of a chart: optional label filter, the name in the legend, and labels to hide. */
export interface ChartQuery {
	metric: string;
	labels?: Record<string, string>;
	/** Legend prefix; series are named "<label> <label values>". */
	label?: string;
	/** Series to leave out, e.g. the idle CPU mode. */
	skip?: (labels: Record<string, string>) => boolean;
}

/** Loads the series of the queries for one node in the range and names them for the legend. */
export async function loadSeries(
	nodeId: string,
	queries: ChartQuery[],
	range: Range,
	fallback = ''
): Promise<ChartSeries[]> {
	const results = await Promise.all(
		queries.map((q) =>
			api.metrics.queryMetrics({
				selector: { metric: q.metric, nodeIds: [nodeId], labels: q.labels ?? {} },
				from: timestampFromMs(range.from),
				to: timestampFromMs(range.to)
			})
		)
	);
	return results.flatMap((res, i) => {
		const q = queries[i];
		return res.series
			.filter((s) => !q.skip?.(s.labels))
			.map((s) => {
				const values = Object.entries(s.labels)
					.filter(([name]) => !(q.labels && name in q.labels))
					.sort(([a], [b]) => a.localeCompare(b))
					.map(([, value]) => value);
				return {
					label: [q.label, ...values].filter(Boolean).join(' ') || fallback || q.metric,
					points: s.points.map((p) => ({ time: p.time ? timestampMs(p.time) : 0, value: p.avg }))
				};
			});
	});
}
