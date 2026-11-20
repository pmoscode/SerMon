import { describe, expect, it } from 'vitest';
import { formatIn } from './forecast.js';

describe('formatIn', () => {
	const now = new Date('2026-11-20T12:00:00Z');
	it.each([
		[30 * 60, 'in 30 minutes', 'in 30 Minuten'],
		[5 * 3600, 'in 5 hours', 'in 5 Stunden'],
		[3 * 86400, 'in 3 days', 'in 3 Tagen'],
		[90 * 86400, 'in 3 months', 'in 3 Monaten'],
		[-60, 'in 1 minute', 'in 1 Minute']
	])('%d s', (seconds, en, de) => {
		const then = new Date(now.getTime() + seconds * 1000);
		expect(formatIn(then, now, 'en')).toBe(en);
		expect(formatIn(then, now, 'de')).toBe(de);
	});
});
