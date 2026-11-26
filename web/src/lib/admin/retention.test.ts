import { describe, expect, it } from 'vitest';
import { checkRetention, parseDays } from './retention.js';

const limits = { logMin: 1, logMax: 14, eventMin: 7, eventMax: 365, cap: 0 };

describe('parseDays', () => {
	it('reads whole days, empty is the default', () => {
		expect(parseDays('')).toBe(0);
		expect(parseDays(' 3 ')).toBe(3);
		expect(parseDays('2.5')).toBeUndefined();
		expect(parseDays('-1')).toBeUndefined();
		expect(parseDays('x')).toBeUndefined();
	});
});

describe('checkRetention', () => {
	it('accepts the defaults and values within the limits', () => {
		expect(checkRetention(0, 0, limits)).toEqual([]);
		expect(checkRetention(1, 7, limits)).toEqual([]);
		expect(checkRetention(14, 365, limits)).toEqual([]);
	});

	it('names what is out of range', () => {
		expect(checkRetention(15, 6, limits)).toEqual(['logs', 'events']);
		expect(checkRetention(undefined, 30, limits)).toEqual(['logs']);
	});

	it('keeps set values within the cap of the organization', () => {
		const capped = { ...limits, cap: 30 };
		expect(checkRetention(14, 30, capped)).toEqual([]);
		expect(checkRetention(0, 31, capped)).toEqual(['events']);
		expect(checkRetention(0, 0, capped)).toEqual([]);
	});
});
