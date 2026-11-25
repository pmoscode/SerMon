import { describe, expect, it } from 'vitest';
import {
	colorOf,
	heatmapRange,
	MAX_RANGE,
	MIN_RANGE,
	moveCursor,
	orderRows,
	SCALE,
	summarize
} from './heatmap.js';

describe('heatmap', () => {
	it('asks for 10 minutes to 30 days, ending where the range ends', () => {
		const to = 100 * 86_400_000;
		expect(heatmapRange({ from: to - 3600_000, to })).toEqual({ from: to - 3600_000, to });
		expect(heatmapRange({ from: to - 60 * 86_400_000, to })).toEqual({ from: to - MAX_RANGE, to });
		expect(heatmapRange({ from: to - 60_000, to })).toEqual({ from: to - MIN_RANGE, to });
	});

	it('colours 0..1 on the scale, nothing for a missing value', () => {
		expect(colorOf(0)).toBe(SCALE[0]);
		expect(colorOf(0.124)).toBe(SCALE[0]);
		expect(colorOf(0.125)).toBe(SCALE[1]);
		expect(colorOf(0.99)).toBe(SCALE[7]);
		expect(colorOf(1)).toBe(SCALE[7]);
		expect(colorOf(3)).toBe(SCALE[7]);
		expect(colorOf(-1)).toBeUndefined();
		expect(colorOf(Number.NaN)).toBeUndefined();
	});

	it('sums a row up without its missing cells', () => {
		expect(summarize([-1, 0.2, 0.6, -1, 0.4])).toEqual({
			mean: expect.closeTo(0.4),
			max: 0.6,
			maxAt: 2
		});
		expect(summarize([-1, -1])).toBeUndefined();
		expect(summarize([0, 0])).toEqual({ mean: 0, max: 0, maxAt: 0 });
	});

	it('orders the rows by name or the highest first, without data last', () => {
		const rows = [
			{ hostname: 'c', values: [0.1, 0.9] },
			{ hostname: 'a', values: [0.5, 0.5] },
			{ hostname: 'd', values: [-1] },
			{ hostname: 'b', values: [0.5, 0.5] }
		];
		expect(orderRows(rows, 'name').map((r) => r.hostname)).toEqual(['a', 'b', 'c', 'd']);
		expect(orderRows(rows, 'max').map((r) => r.hostname)).toEqual(['c', 'a', 'b', 'd']);
		expect(orderRows(rows, 'mean').map((r) => r.hostname)).toEqual(['a', 'b', 'c', 'd']);
	});

	it('moves the cursor within the grid', () => {
		const at = { row: 1, column: 1 };
		expect(moveCursor('ArrowLeft', at, 3, 5)).toEqual({ row: 1, column: 0 });
		expect(moveCursor('ArrowLeft', { row: 1, column: 0 }, 3, 5)).toEqual({ row: 1, column: 0 });
		expect(moveCursor('ArrowRight', at, 3, 5)).toEqual({ row: 1, column: 2 });
		expect(moveCursor('ArrowDown', { row: 2, column: 1 }, 3, 5)).toEqual({ row: 2, column: 1 });
		expect(moveCursor('ArrowUp', at, 3, 5)).toEqual({ row: 0, column: 1 });
		expect(moveCursor('End', at, 3, 5)).toEqual({ row: 1, column: 4 });
		expect(moveCursor('Home', at, 3, 5)).toEqual({ row: 1, column: 0 });
		expect(moveCursor('PageDown', at, 30, 5)).toEqual({ row: 11, column: 1 });
		expect(moveCursor('PageUp', at, 30, 5)).toEqual({ row: 0, column: 1 });
		expect(moveCursor('a', at, 3, 5)).toBeUndefined();
	});
});
