import { create } from '@bufbuild/protobuf';
import { timestampFromMs } from '@bufbuild/protobuf/wkt';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { anomaliesApi } from '#lib/api/anomalies.js';
import { GetNodeAnomaliesResponseSchema } from '#lib/api/gen/sermon/web/v1/anomaly_pb.js';
import { NodeAnomalies } from './node.svelte.js';

const day = 86_400_000;
const now = 100 * day;

describe('NodeAnomalies', () => {
	afterEach(() => vi.restoreAllMocks());

	it('loads the band of the last 7 days of the range and the learning day', async () => {
		const get = vi.spyOn(anomaliesApi, 'getNodeAnomalies').mockResolvedValue(
			create(GetNodeAnomaliesResponseSchema, {
				learningDay: 3,
				series: [
					{
						metric: 'sermon_cpu_utilization_ratio',
						labels: { mode: 'user' },
						intervals: [{ from: timestampFromMs(now - 60_000), to: timestampFromMs(now) }]
					}
				]
			})
		);
		const a = new NodeAnomalies('n1');
		await a.load({ from: now - 20 * day, to: now }, now);
		const req = get.mock.calls[0][0];
		expect(req.nodeId).toBe('n1');
		expect(Number(req.from?.seconds) * 1000).toBe(now - 7 * day);
		expect(a.learningDay).toBe(3);
		expect(a.series).toEqual([
			{
				metric: 'sermon_cpu_utilization_ratio',
				labels: { mode: 'user' },
				intervals: [{ from: now - 60_000, to: now }]
			}
		]);
		expect(a.loaded && !a.failed).toBe(true);
	});

	it('keeps the charts as they are when it fails, and a newer load wins', async () => {
		let resolveFirst: (v: unknown) => void = () => {};
		const get = vi
			.spyOn(anomaliesApi, 'getNodeAnomalies')
			.mockImplementationOnce(() => new Promise((r) => (resolveFirst = r)) as never)
			.mockResolvedValueOnce(create(GetNodeAnomaliesResponseSchema, { learningDay: 5 }));
		const a = new NodeAnomalies('n1');
		const first = a.load({ from: now - day, to: now }, now);
		await a.load({ from: now - day, to: now }, now);
		resolveFirst(create(GetNodeAnomaliesResponseSchema, { learningDay: 1 }));
		await first;
		expect(a.learningDay).toBe(5);
		get.mockRejectedValueOnce(new Error('down'));
		await a.load({ from: now - day, to: now }, now);
		expect(a.failed).toBe(true);
		expect(a.learningDay).toBe(5);
	});

	it('shows no band for a range older than 30 days', async () => {
		vi.spyOn(anomaliesApi, 'getNodeAnomalies').mockResolvedValue(
			create(GetNodeAnomaliesResponseSchema, {
				series: [{ metric: 'm', intervals: [{ from: timestampFromMs(1), to: timestampFromMs(2) }] }]
			})
		);
		const a = new NodeAnomalies('n1');
		await a.load({ from: now - 40 * day, to: now - 35 * day }, now);
		expect(a.series).toEqual([]);
	});
});
