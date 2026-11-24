import { describe, expect, it } from 'vitest';
import { stack } from './chart.js';

const min = 60_000;
const range = { from: 0, to: 10 * min };

describe('stacked failed logins', () => {
	it('puts each count into the bar of its bucket start, busiest node first', () => {
		const s = stack(
			[
				{ node: 'b', points: [{ at: 0, count: 1 }] },
				{
					node: 'a',
					points: [
						{ at: 0, count: 2 },
						{ at: 9 * min, count: 3 }
					]
				}
			],
			min,
			range
		);
		expect(s.legend).toEqual([
			{ node: 'a', total: 5 },
			{ node: 'b', total: 1 }
		]);
		expect(s.bars).toHaveLength(10);
		expect(s.bars[0]).toEqual({ start: 0, end: min, counts: [2, 1], total: 3 });
		expect(s.bars[9].counts).toEqual([3, 0]);
	});

	it('counts a point in the bar it starts in', () => {
		const s = stack([{ node: 'a', points: [{ at: 1.6 * min, count: 1 }] }], min, range);
		expect(s.bars[1].total).toBe(1);
	});

	it('gathers the nodes beyond the bound in one entry', () => {
		const series = ['n1', 'n2', 'n3', 'n4'].map((node, i) => ({
			node,
			points: [{ at: min, count: 4 - i }]
		}));
		const s = stack(series, min, range, 2);
		expect(s.legend).toEqual([{ node: 'n1', total: 4 }, { node: 'n2', total: 3 }, { total: 3 }]);
		expect(s.bars[1].counts).toEqual([4, 3, 3]);
	});

	it('leaves out counts outside the range and nodes without any', () => {
		const s = stack(
			[
				{
					node: 'a',
					points: [
						{ at: -min, count: 5 },
						{ at: 10 * min, count: 5 },
						{ at: 2 * min, count: Number.NaN }
					]
				},
				{ node: 'b', points: [] }
			],
			min,
			range
		);
		expect(s.legend).toEqual([]);
		expect(s.bars.every((b) => b.total === 0)).toBe(true);
	});

	it('bounds the bars and handles a broken step', () => {
		expect(stack([], min, { from: 0, to: 1000 * min }).bars).toHaveLength(400);
		expect(stack([], 0, range).bars).toEqual([]);
		expect(stack([], min, { from: 5, to: 5 }).bars).toEqual([]);
	});
});
