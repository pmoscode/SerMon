import { timestampFromMs, timestampMs } from '@bufbuild/protobuf/wkt';
import { untrack } from 'svelte';
import { api } from '#lib/api/client.js';
import type { ChartPoint } from '#lib/charts/data.js';
import type { Range } from '#lib/stores/timerange.svelte.js';
import { combine, type CompareMetric } from './compare.js';

/** One metric of the comparison: one line per node, or why it is missing. */
export interface CompareResult {
	perNode: ReadonlyMap<string, ChartPoint[]>;
	error: boolean;
}

/**
 * Loads the metrics of the comparison for the chosen nodes: one QueryMetrics per metric for all
 * nodes, so the nodes share the server's step and line up point by point.
 */
export class CompareState {
	results = $state<Record<string, CompareResult>>({});
	loading = $state(false);
	#request = 0;

	async load(nodeIds: string[], metrics: CompareMetric[], range: Range): Promise<void> {
		const current = ++this.#request;
		if (nodeIds.length === 0) {
			this.results = {};
			this.loading = false;
			return;
		}
		// The first load shows the spinner; refreshes keep the charts (untracked: no loop in effects).
		if (untrack(() => Object.keys(this.results).length === 0)) this.loading = true;
		const entries = await Promise.all(
			metrics.map(async (metric): Promise<[string, CompareResult]> => {
				try {
					const res = await api.metrics.queryMetrics({
						selector: { metric: metric.metric, nodeIds, labels: metric.labels ?? {} },
						from: timestampFromMs(range.from),
						to: timestampFromMs(range.to)
					});
					const series = res.series
						.filter((s) => nodeIds.includes(s.nodeId))
						.map((s) => ({
							nodeId: s.nodeId,
							points: s.points.map((p) => ({
								time: p.time ? timestampMs(p.time) : 0,
								value: p.avg
							}))
						}));
					return [
						metric.key,
						{ perNode: combine(series, metric.combine, metric.transform), error: false }
					];
				} catch {
					return [metric.key, { perNode: combine([], metric.combine), error: true }];
				}
			})
		);
		if (current !== this.#request) return;
		this.results = Object.fromEntries(entries);
		this.loading = false;
	}
}
