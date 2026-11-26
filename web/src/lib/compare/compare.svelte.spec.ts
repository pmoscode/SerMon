import { create } from '@bufbuild/protobuf';
import { timestampFromMs } from '@bufbuild/protobuf/wkt';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { api } from '#lib/api/client.js';
import { QueryMetricsResponseSchema } from '#lib/api/gen/sermon/web/v1/metrics_pb.js';
import { compareMetrics } from './compare.js';
import { CompareState } from './compare.svelte.js';

const a = '0193f5a0-0000-7000-8000-000000000001';
const b = '0193f5a0-0000-7000-8000-000000000002';
const other = '0193f5a0-0000-7000-8000-0000000000ff';
const range = { from: 1_000_000, to: 2_000_000 };

function answer(nodes: string[]) {
	return create(QueryMetricsResponseSchema, {
		series: nodes.map((nodeId) => ({
			nodeId,
			points: [{ time: timestampFromMs(1_500_000), avg: 250_000 }]
		}))
	});
}

describe('CompareState', () => {
	afterEach(() => vi.restoreAllMocks());

	it('asks once per metric for all nodes, also inside an effect', async () => {
		const query = vi.spyOn(api.metrics, 'queryMetrics').mockResolvedValue(answer([a, b]));
		const state = new CompareState();
		const stop = $effect.root(() => {
			$effect(() => void state.load([a, b], compareMetrics, range));
		});
		await vi.waitFor(() => expect(state.results.cpu).toBeDefined());
		await new Promise((r) => setTimeout(r, 50));
		stop();
		expect(query).toHaveBeenCalledTimes(compareMetrics.length);
		expect(query.mock.calls[0][0].selector?.nodeIds).toEqual([a, b]);
		// CPU busy from idle.
		expect(state.results.cpu.perNode.get(a)).toEqual([{ time: 1_500_000, value: 750_000 }]);
	});

	it('drops series of nodes it did not ask for', async () => {
		vi.spyOn(api.metrics, 'queryMetrics').mockResolvedValue(answer([a, other]));
		const state = new CompareState();
		await state.load([a, b], compareMetrics, range);
		expect([...state.results.memory.perNode.keys()]).toEqual([a]);
	});

	it('marks a failed metric and keeps the others', async () => {
		vi.spyOn(api.metrics, 'queryMetrics').mockImplementation(async (req) => {
			if (req.selector?.metric === 'sermon_memory_utilization_ratio') throw new Error('down');
			return answer([a, b]);
		});
		const state = new CompareState();
		await state.load([a, b], compareMetrics, range);
		expect(state.results.memory.error).toBe(true);
		expect(state.results.cpu.error).toBe(false);
		expect(state.loading).toBe(false);
	});

	it('asks nothing without nodes', async () => {
		const query = vi.spyOn(api.metrics, 'queryMetrics');
		const state = new CompareState();
		await state.load([], compareMetrics, range);
		expect(query).not.toHaveBeenCalled();
		expect(state.results).toEqual({});
	});
});
