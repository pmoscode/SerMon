import { timestampFromMs } from '@bufbuild/protobuf/wkt';
import { api, Code, codeOf } from '#lib/api/client.js';
import type { GetFleetSummaryResponse } from '#lib/api/gen/sermon/web/v1/fleet_pb.js';

/** How often the overview is refreshed (live update) and how often the sparklines. */
export const SUMMARY_INTERVAL = 10_000;
export const SPARKLINE_INTERVAL = 30_000;
/** Sparklines cover the last 15 minutes in 30 s steps (KONZEPT §16). */
const SPARKLINE_SPAN = 15 * 60_000;
const SPARKLINE_STEP = 30n;
/** QueryMetrics accepts at most 100 nodes per call. */
const NODES_PER_QUERY = 100;

/** The fleet overview: summary, CPU sparklines and their refresh. */
export class FleetState {
	summary = $state<GetFleetSummaryResponse | undefined>(undefined);
	/** CPU busy (0..1) of the last 15 minutes per node, oldest first. */
	sparklines = $state<Record<string, number[]>>({});
	status = $state<'loading' | 'ready' | 'error'>('loading');
	/** The last refresh failed while older data is still shown. */
	stale = $state(false);
	/** Time of the last successful refresh (ms since the epoch). */
	updatedAt = $state<number | undefined>(undefined);

	async load(): Promise<void> {
		try {
			this.summary = await api.fleet.getFleetSummary({});
			this.status = 'ready';
			this.stale = false;
			this.updatedAt = Date.now();
		} catch (error) {
			// An ended session is handled by the expiry interceptor (login page).
			if (codeOf(error) === Code.Unauthenticated) return;
			if (this.summary) this.stale = true;
			else this.status = 'error';
		}
	}

	async loadSparklines(now = Date.now()): Promise<void> {
		const ids = (this.summary?.nodes ?? []).filter((n) => n.online).map((n) => n.nodeId);
		const next: Record<string, number[]> = {};
		for (let i = 0; i < ids.length; i += NODES_PER_QUERY) {
			try {
				const res = await api.metrics.queryMetrics({
					selector: {
						metric: 'sermon_cpu_utilization_ratio',
						nodeIds: ids.slice(i, i + NODES_PER_QUERY),
						labels: { mode: 'idle' }
					},
					from: timestampFromMs(now - SPARKLINE_SPAN),
					to: timestampFromMs(now),
					step: { seconds: SPARKLINE_STEP, nanos: 0 }
				});
				for (const series of res.series) {
					next[series.nodeId] = series.points.map((p) => Math.min(1, Math.max(0, 1 - p.avg / 1e6)));
				}
			} catch {
				// Sparklines are decoration: the figures stay without them.
			}
		}
		this.sparklines = next;
	}

	/** Loads now and keeps refreshing; returns the function that stops it. */
	start(): () => void {
		void this.load().then(() => this.loadSparklines());
		const summary = setInterval(() => void this.load(), SUMMARY_INTERVAL);
		const sparklines = setInterval(() => void this.loadSparklines(), SPARKLINE_INTERVAL);
		return () => {
			clearInterval(summary);
			clearInterval(sparklines);
		};
	}
}
