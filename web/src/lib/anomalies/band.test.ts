import { describe, expect, it } from 'vitest';
import { bandRange, bandsFor, merge, MAX_RANGE, RETENTION, type AnomalousSeries } from './band.js';

const min = 60_000;

describe('anomaly band', () => {
	it('merges touching and overlapping stretches in order', () => {
		expect(
			merge([
				{ from: 10 * min, to: 12 * min },
				{ from: 1 * min, to: 3 * min },
				{ from: 3 * min, to: 4 * min },
				{ from: 11 * min, to: 15 * min }
			])
		).toEqual([
			{ from: 1 * min, to: 4 * min },
			{ from: 10 * min, to: 15 * min }
		]);
		expect(merge([])).toEqual([]);
	});

	it('shows the series of the chart only, with its label filter and skip', () => {
		const series: AnomalousSeries[] = [
			{ metric: 'cpu', labels: { mode: 'user' }, intervals: [{ from: 1, to: 2 }] },
			{ metric: 'cpu', labels: { mode: 'idle' }, intervals: [{ from: 5, to: 6 }] },
			{ metric: 'mem', labels: {}, intervals: [{ from: 8, to: 9 }] }
		];
		const notIdle = (l: Record<string, string>) => l.mode === 'idle';
		expect(bandsFor(series, [{ metric: 'cpu', skip: notIdle }])).toEqual([{ from: 1, to: 2 }]);
		expect(bandsFor(series, [{ metric: 'cpu', labels: { mode: 'idle' } }])).toEqual([
			{ from: 5, to: 6 }
		]);
		expect(bandsFor(series, [{ metric: 'cpu' }, { metric: 'mem' }])).toHaveLength(3);
		expect(bandsFor(series, [{ metric: 'disk' }])).toEqual([]);
	});

	it('asks for at most the last 7 days within the last 30', () => {
		const now = 100 * 86_400_000;
		expect(bandRange({ from: now - 3600_000, to: now }, now)).toEqual({
			from: now - 3600_000,
			to: now
		});
		expect(bandRange({ from: now - 20 * 86_400_000, to: now }, now)).toEqual({
			from: now - MAX_RANGE,
			to: now
		});
		expect(
			bandRange({ from: now - 40 * 86_400_000, to: now - 31 * 86_400_000 }, now)
		).toBeUndefined();
		expect(bandRange({ from: now - 40 * 86_400_000, to: now - 29 * 86_400_000 }, now)?.from).toBe(
			now - RETENTION + 60_000
		);
	});
});
