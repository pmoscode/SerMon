import { timestampFromMs, timestampMs } from '@bufbuild/protobuf/wkt';
import { getContext, setContext } from 'svelte';
import { anomaliesApi } from '#lib/api/anomalies.js';
import type { Range } from '#lib/stores/timerange.svelte.js';
import { bandRange, type AnomalousSeries } from './band.js';

/**
 * The anomalies of one node for the page's range (M4-25): loaded once and read by every chart of
 * the page through the context.
 */
export class NodeAnomalies {
	series = $state<AnomalousSeries[]>([]);
	/** The day of the learning phase (1 to 7); 0 when it is over. */
	learningDay = $state(0);
	loaded = $state(false);
	failed = $state(false);
	#request = 0;

	constructor(readonly nodeId: string) {}

	/** Loads the band of range (its last 7 days within the last 30); a newer load wins. */
	async load(range: Range, now = Date.now()): Promise<void> {
		const current = ++this.#request;
		const shown = bandRange(range, now);
		try {
			const res = await anomaliesApi.getNodeAnomalies({
				nodeId: this.nodeId,
				from: timestampFromMs(shown?.from ?? now - 60_000),
				to: timestampFromMs(shown?.to ?? now)
			});
			if (current !== this.#request) return;
			this.learningDay = res.learningDay;
			this.series = shown
				? res.series.map((s) => ({
						metric: s.metric,
						labels: s.labels,
						intervals: s.intervals.map((i) => ({
							from: i.from ? timestampMs(i.from) : 0,
							to: i.to ? timestampMs(i.to) : 0
						}))
					}))
				: [];
			this.failed = false;
		} catch {
			// The band is an addition: without it the charts stay as they are.
			if (current === this.#request) this.failed = true;
		} finally {
			if (current === this.#request) this.loaded = true;
		}
	}
}

const KEY = Symbol('node-anomalies');

export function provideNodeAnomalies(state: NodeAnomalies): void {
	setContext(KEY, state);
}

/** The anomalies of the page's node; undefined outside a node page. */
export function nodeAnomalies(): NodeAnomalies | undefined {
	return getContext<NodeAnomalies | undefined>(KEY);
}
