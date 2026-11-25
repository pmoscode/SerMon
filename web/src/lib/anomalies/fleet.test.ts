import { describe, expect, it, vi } from 'vitest';
import { create } from '@bufbuild/protobuf';
import { api } from '#lib/api/client.js';
import { QueryMetricsResponseSchema } from '#lib/api/gen/sermon/web/v1/metrics_pb.js';
import { fleetRate, loadRates } from './fleet.js';

describe('fleet anomaly rates', () => {
	it('reads the latest and highest rate per node in chunks of 100 nodes', async () => {
		const ids = Array.from({ length: 150 }, (_, i) => `n${i}`);
		const query = vi.spyOn(api.metrics, 'queryMetrics').mockImplementation(async (req) => {
			const nodes = req.selector?.nodeIds ?? [];
			return create(QueryMetricsResponseSchema, {
				series: nodes.includes('n0')
					? [{ nodeId: 'n0', points: [{ avg: 300_000 }, { avg: 100_000 }] }]
					: []
			});
		});
		const rates = await loadRates(ids, 1_000_000);
		expect(query).toHaveBeenCalledTimes(2);
		expect(query.mock.calls[0][0].selector?.nodeIds).toHaveLength(100);
		expect(rates).toEqual({ n0: { last: 0.1, max: 0.3 } });
	});

	it('averages the latest rates of the fleet', () => {
		expect(fleetRate({})).toBeUndefined();
		expect(fleetRate({ a: { last: 0.1, max: 0.5 }, b: { last: 0.3, max: 0.3 } })).toBeCloseTo(0.2);
	});
});
