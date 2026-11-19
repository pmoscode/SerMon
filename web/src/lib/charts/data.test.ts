import { describe, expect, it } from 'vitest';
import { align, summarize } from './data.js';

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
