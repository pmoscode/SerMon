import { timestampFromMs } from '@bufbuild/protobuf/wkt';
import { api } from '#lib/api/client.js';

/** The anomaly rate (0..1) of a node in the last minutes: the latest value and the highest. */
export interface NodeRate {
	last: number;
	max: number;
}

/** The written rate is in ppm (ADR 0037). */
const PPM = 1e6;
/** QueryMetrics accepts at most 100 nodes per call. */
const NODES_PER_QUERY = 100;
/** The dashboard looks at the last 15 minutes. */
export const RATE_SPAN = 15 * 60_000;

/** Reads the anomaly rates of the nodes; nodes without a rate (learning, no candidates) are missing. */
export async function loadRates(
	nodeIds: string[],
	now = Date.now()
): Promise<Record<string, NodeRate>> {
	const out: Record<string, NodeRate> = {};
	for (let i = 0; i < nodeIds.length; i += NODES_PER_QUERY) {
		const res = await api.metrics.queryMetrics({
			selector: {
				metric: 'sermon_anomaly_rate_ratio',
				nodeIds: nodeIds.slice(i, i + NODES_PER_QUERY)
			},
			from: timestampFromMs(now - RATE_SPAN),
			to: timestampFromMs(now)
		});
		for (const s of res.series) {
			const values = s.points.map((p) => p.avg / PPM);
			if (values.length > 0) out[s.nodeId] = { last: values.at(-1) ?? 0, max: Math.max(...values) };
		}
	}
	return out;
}

/** The fleet's anomaly rate: the mean of its nodes' latest rates; undefined without any. */
export function fleetRate(rates: Record<string, NodeRate>): number | undefined {
	const values = Object.values(rates).map((r) => r.last);
	return values.length === 0 ? undefined : values.reduce((a, b) => a + b, 0) / values.length;
}
