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

	it('stays bounded for odd steps', () => {
		expect(buckets([], 0, { from, to })).toEqual([]);
		expect(buckets([], 1, { from, to: from + 86400_000 }).length).toBeLessThanOrEqual(1000);
	});
});
