import { describe, expect, it } from 'vitest';
import { LogSeverity } from '#lib/api/gen/sermon/web/v1/log_settings_pb.js';
import { buckets, groupOf } from './histogram.js';

const from = Date.parse('2026-11-23T11:00:00Z');
const to = from + 10 * 60_000;
const step = 60_000;

describe('groupOf', () => {
	it('puts the severities into four groups', () => {
		expect(groupOf(LogSeverity.EMERGENCY)).toBe('error');
		expect(groupOf(LogSeverity.ERROR)).toBe('error');
		expect(groupOf(LogSeverity.WARNING)).toBe('warning');
		expect(groupOf(LogSeverity.NOTICE)).toBe('info');
		expect(groupOf(LogSeverity.INFO)).toBe('info');
		expect(groupOf(LogSeverity.DEBUG)).toBe('debug');
		expect(groupOf(LogSeverity.UNSPECIFIED)).toBe('debug');
	});
});

describe('buckets', () => {
	it('places each count in the bucket that ends at its time', () => {
		const b = buckets(
			[
				{
					severity: LogSeverity.ERROR,
					points: [
						{ at: from + step, count: 3 },
						{ at: from + 3 * step, count: 1 }
					]
				},
				{ severity: LogSeverity.CRITICAL, points: [{ at: from + step, count: 2 }] },
				{ severity: LogSeverity.INFO, points: [{ at: to, count: 7 }] },
				// Before the range: Loki's first point covers the step before it.
				{ severity: LogSeverity.DEBUG, points: [{ at: from, count: 9 }] }
			],
			step,
			{ from, to }
		);
		expect(b).toHaveLength(10);
		expect(b[0]).toEqual({
			start: from,
			end: from + step,
			counts: { error: 5, warning: 0, info: 0, debug: 0 },
			total: 5
		});
		expect(b[2].counts.error).toBe(1);
		expect(b[9].counts.info).toBe(7);
		expect(b.reduce((sum, x) => sum + x.counts.debug, 0)).toBe(0);
	});

	// V1-24: long ranges come from the counts of the ingest at points aligned to the step, not to
	// the start of the range. The first point lies less than a step after the start – its lines
	// are in the range and belong in the first bucket; the last may lie after the end.
	it('places points aligned to the step in the bucket around their time', () => {
		const hour = 3_600_000;
		const start = Date.parse('2026-11-13T22:16:00Z');
		const end = start + 14 * 24 * hour;
		const firstAligned = Date.parse('2026-11-14T00:00:00Z');
		const b = buckets(
			[
				{
					severity: LogSeverity.ERROR,
					points: [
						{ at: start, count: 9 }, // the step before the range
						{ at: firstAligned, count: 2 },
						{ at: firstAligned + 4 * hour, count: 3 },
						{ at: Date.parse('2026-11-28T00:00:00Z'), count: 7 }
					]
				}
			],
			4 * hour,
			{ from: start, to: end }
		);
		expect(b).toHaveLength(84);
		expect(b[0].counts.error).toBe(2);
		expect(b[1].counts.error).toBe(3);
		expect(b[83].counts.error).toBe(7);
		expect(b.reduce((sum, x) => sum + x.total, 0)).toBe(12);
	});

	it('stays bounded for odd steps', () => {
		expect(buckets([], 0, { from, to })).toEqual([]);
		expect(buckets([], 1, { from, to: from + 86400_000 }).length).toBeLessThanOrEqual(1000);
	});
});
