import { describe, expect, it } from 'vitest';
import {
	formatBitsPerSecond,
	formatBytes,
	formatDuration,
	formatNumber,
	formatPercent,
	formatValue,
	type Unit
} from './format.js';

/** Intl uses no-break and narrow no-break spaces; compare with plain spaces. */
const plain = (text: string) => text.replace(/[\u00a0\u202f]/g, ' ');

describe('formatBytes (IEC)', () => {
	it.each([
		[0, '0 B', '0 B'],
		[512, '512 B', '512 B'],
		[1536, '1.5 KiB', '1,5 KiB'],
		[1023.9, '1 KiB', '1 KiB'],
		[10 * 1024 ** 2 + 300 * 1024, '10.3 MiB', '10,3 MiB'],
		[512 * 1024 ** 3, '512 GiB', '512 GiB'],
		[2.25 * 1024 ** 4, '2.25 TiB', '2,25 TiB'],
		[Number.NaN, '–', '–']
	])('%d bytes', (value, en, de) => {
		expect(plain(formatBytes(value, 'en'))).toBe(en);
		expect(plain(formatBytes(value, 'de'))).toBe(de);
	});
});

describe('formatBitsPerSecond (SI)', () => {
	it.each([
		[999, '999 bit/s', '999 bit/s'],
		[1000, '1 kbit/s', '1 kbit/s'],
		[125_500_000, '126 Mbit/s', '126 Mbit/s'],
		[2.5e9, '2.5 Gbit/s', '2,5 Gbit/s']
	])('%d bit/s', (value, en, de) => {
		expect(plain(formatBitsPerSecond(value, 'en'))).toBe(en);
		expect(plain(formatBitsPerSecond(value, 'de'))).toBe(de);
	});
});

describe('formatPercent', () => {
	it.each([
		[0, '0%', '0 %'],
		[0.005, '0.5%', '0,5 %'],
		[0.42, '42%', '42 %'],
		[1, '100%', '100 %']
	])('%d', (value, en, de) => {
		expect(plain(formatPercent(value, 'en'))).toBe(en);
		expect(plain(formatPercent(value, 'de'))).toBe(de);
	});
});

describe('formatDuration', () => {
	it.each([
		[0.004, '4 ms', '4 ms'],
		[42, '42 sec', '42 Sek.'],
		[303, '5 min 3 sec', '5 Min. 3 Sek.'],
		[3600, '1 hr', '1 Std.'],
		[3 * 86400 + 4 * 3600 + 59, '3 days 4 hr', '3 Tg. 4 Std.']
	])('%d s', (value, en, de) => {
		expect(plain(formatDuration(value, 'en'))).toBe(en);
		expect(plain(formatDuration(value, 'de'))).toBe(de);
	});
});

describe('formatNumber', () => {
	it.each([
		[1.234, '1.23', '1,23'],
		[12.34, '12.3', '12,3'],
		[1234567, '1,234,567', '1.234.567']
	])('%d', (value, en, de) => {
		expect(formatNumber(value, 'en')).toBe(en);
		expect(formatNumber(value, 'de')).toBe(de);
	});
});

describe('formatValue by catalog unit', () => {
	it.each<[Unit, number, string, string]>([
		['ratio_ppm', 250_000, '25%', '25 %'],
		['ratio', 0.9, '90%', '90 %'],
		['bytes_per_second', 2048, '2 KiB/s', '2 KiB/s'],
		['per_second', 12.5, '12.5 /s', '12,5 /s'],
		['pages_per_second', 3, '3 pages/s', '3 Seiten/s'],
		['load', 0.5, '0.50', '0,50'],
		['celsius', 45.25, '45.3°C', '45,3 °C'],
		['boolean', 1, 'yes', 'ja'],
		['boolean', 0, 'no', 'nein'],
		['seconds', 90, '1 min 30 sec', '1 Min. 30 Sek.'],
		['count', 7, '7', '7']
	])('%s %d', (unit, value, en, de) => {
		expect(plain(formatValue(unit, value, 'en'))).toBe(en);
		expect(plain(formatValue(unit, value, 'de'))).toBe(de);
	});

	it('shows byte rates as bit/s on request', () => {
		expect(plain(formatValue('bytes_per_second', 12_500_000, 'en', { bits: true }))).toBe(
			'100 Mbit/s'
		);
	});
});
