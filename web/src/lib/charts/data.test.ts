import { describe, expect, it } from 'vitest';
import { align, summarize, yRange, ySplits } from './data.js';

describe('align', () => {
	it('puts series of different nodes on one time axis with gaps', () => {
		const aligned = align([
			{
				label: 'web-01',
				points: [
					{ time: 2000, value: 2 },
					{ time: 1000, value: 1 }
				]
			},
			{
				label: 'db-01',
				points: [
					{ time: 3000, value: 30 },
					{ time: 2000, value: Number.NaN }
				]
			}
		]);

		expect(aligned).toEqual([
			[1, 2, 3],
			[1, 2, null],
			[null, null, 30]
		]);
	});

	it('handles no series', () => {
		expect(align([])).toEqual([[]]);
	});
});

describe('summarize', () => {
	const series = [
		{
			label: 'a',
			points: [
				{ time: 1000, value: 5 },
				{ time: 3000, value: 1 },
				{ time: 2000, value: 9 }
			]
		},
		{ label: 'empty', points: [] }
	];

	it('reports min, max and the latest value within the range', () => {
		expect(summarize(series, { from: 0, to: 4000 })).toEqual([
			{ label: 'a', min: 1, max: 9, last: 1 }
		]);
		expect(summarize(series, { from: 0, to: 2000 })).toEqual([
			{ label: 'a', min: 5, max: 5, last: 5 }
		]);
		expect(summarize(series, { from: 5000, to: 6000 })).toEqual([]);
	});
});

describe('yRange', () => {
	it('starts at zero and keeps the data range', () => {
		expect(yRange('bytes', 100, 500)).toEqual([0, 500]);
		expect(yRange('load', -0.5, 2)).toEqual([-0.5, 2]);
	});

	it('spans one unit step for flat or missing data, 100 % for shares', () => {
		expect(yRange('ratio_ppm', null, null)).toEqual([0, 1_000_000]);
		expect(yRange('ratio_ppm', 0, 0)).toEqual([0, 1_000_000]);
		expect(yRange('ratio_ppm', 300_000, 300_000)).toEqual([0, 1_000_000]);
		expect(yRange('count', 3, 3)).toEqual([0, 4]);
		expect(yRange('bytes', null, null)).toEqual([0, 1]);
	});

	// M5-32: up/down values (sermon_nginx_up) read "no" and "yes", nothing between or above.
	it('spans exactly no to yes for booleans, with a tick at each', () => {
		expect(yRange('boolean', 1, 1)).toEqual([0, 1]);
		expect(yRange('boolean', 0, 1)).toEqual([0, 1]);
		expect(yRange('boolean', null, null)).toEqual([0, 1]);
		expect(yRange('boolean', 1, 1, 1)).toEqual([0, 1]);
		expect(ySplits('boolean')).toEqual([0, 1]);
		expect(ySplits('count')).toBeUndefined();
	});

	// M5-28: side by side, every chart of a metric has the same scale.
	it('reaches up to a shared maximum of other charts', () => {
		expect(yRange('bytes', 100, 500, 2000)).toEqual([0, 2000]);
		expect(yRange('bytes', 100, 500, 300)).toEqual([0, 500]);
		expect(yRange('bytes', null, null, 2000)).toEqual([0, 2000]);
		expect(yRange('ratio_ppm', 0, 0, 400_000)).toEqual([0, 400_000]);
	});
});
