import { describe, expect, it } from 'vitest';
import { Weekday } from '#lib/api/gen/sermon/web/v1/silence_pb.js';
import {
	coversGroups,
	durationText,
	labelsText,
	minutesOfTime,
	parseLabels,
	parsePatterns,
	patternsText,
	secretUpdate,
	timeOfMinutes,
	weekdays
} from './forms.js';

describe('notification forms', () => {
	it('reads rule patterns separated by commas, spaces or lines', () => {
		expect(parsePatterns(' security.*, system.cpu.usage_high\nsystem.memory.* ')).toEqual([
			'security.*',
			'system.cpu.usage_high',
			'system.memory.*'
		]);
		expect(parsePatterns('  ')).toEqual([]);
		expect(parsePatterns('a.b.c, a.b.c')).toEqual(['a.b.c']);
		expect(patternsText(['security.*', 'system.*'])).toBe('security.*, system.*');
	});

	it('reads labels as key=value lines and reports the first bad line', () => {
		expect(parseLabels('category=security\n team = ops \n')).toEqual({
			labels: { category: 'security', team: 'ops' }
		});
		expect(parseLabels('category')).toEqual({ error: 1 });
		expect(parseLabels('a=b\n=c')).toEqual({ error: 2 });
		expect(labelsText({ team: 'ops', category: 'security' })).toBe('category=security\nteam=ops');
	});

	it('converts times of day and durations', () => {
		expect(minutesOfTime('03:30')).toBe(210);
		expect(minutesOfTime('')).toBeUndefined();
		expect(timeOfMinutes(210)).toBe('03:30');
		expect(timeOfMinutes(0)).toBe('00:00');
		expect(durationText(150)).toBe('2 h 30 min');
		expect(durationText(60)).toBe('1 h');
		expect(durationText(20)).toBe('20 min');
	});

	it('lists the weekdays from Monday', () => {
		expect(weekdays.map((d) => d.day)).toEqual([
			Weekday.MONDAY,
			Weekday.TUESDAY,
			Weekday.WEDNESDAY,
			Weekday.THURSDAY,
			Weekday.FRIDAY,
			Weekday.SATURDAY,
			Weekday.SUNDAY
		]);
	});

	// Secrets are write-only: an empty field keeps the stored value, so it is not sent.
	it('sends only the secrets that were typed', () => {
		expect(secretUpdate({ token: '', url: 'https://hooks.example/x' })).toEqual({
			url: 'https://hooks.example/x'
		});
	});
});

describe('silence rights', () => {
	it('covers groups by the session list', () => {
		expect(coversGroups(['*'], [])).toBe(true);
		expect(coversGroups(['g1'], ['g1'])).toBe(true);
		expect(coversGroups(['g1'], ['g1', 'g2'])).toBe(false);
		// A silence without groups mutes all groups: only with "*".
		expect(coversGroups(['g1'], [])).toBe(false);
	});
});
