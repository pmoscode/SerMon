/**
 * Formats metric values for display, by unit of the metric catalog and in the UI language
 * (KONZEPT §17): bytes in IEC steps (KiB, MiB, …), bit rates in SI steps (kbit/s, Mbit/s, …),
 * shares as percent, durations in the two largest units. Number and unit are joined by a
 * no-break space so they never wrap apart.
 */
import { m } from '#lib/paraglide/messages.js';
import type { Locale } from '#lib/paraglide/runtime.js';

/** Units of the metric catalog (server/internal/domain/metric) plus "ratio" for shares 0..1. */
export type Unit =
	| 'ratio'
	| 'ratio_ppm'
	| 'bytes'
	| 'bytes_per_second'
	| 'bits_per_second'
	| 'per_second'
	| 'pages_per_second'
	| 'count'
	| 'seconds'
	| 'timestamp_seconds'
	| 'celsius'
	| 'load'
	| 'boolean';

const NBSP = '\u00a0';
const IEC = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB', 'EiB'];
const BITS = ['bit/s', 'kbit/s', 'Mbit/s', 'Gbit/s', 'Tbit/s', 'Pbit/s'];

/** Shows about three significant digits: 1.23, 12.3, 123. */
function digits(value: number): number {
	const abs = Math.abs(value);
	if (abs === 0 || abs >= 100) return 0;
	return abs >= 10 ? 1 : 2;
}

/** A plain number with up to two decimals (fewer for larger numbers). */
export function formatNumber(value: number, locale: Locale): string {
	if (!Number.isFinite(value)) return '–';
	return new Intl.NumberFormat(locale, { maximumFractionDigits: digits(value) }).format(value);
}

function scaled(value: number, base: number, units: string[], locale: Locale): string {
	if (!Number.isFinite(value)) return '–';
	let step = 0;
	let scaledValue = value;
	while (Math.abs(scaledValue) >= base && step < units.length - 1) {
		scaledValue /= base;
		step++;
	}
	// Rounding can reach the next step (1023.9 B → "1,024 B"): move up instead.
	if (step < units.length - 1 && Number(scaledValue.toFixed(digits(scaledValue))) >= base) {
		scaledValue /= base;
		step++;
	}
	const fraction = step === 0 && Number.isInteger(scaledValue) ? 0 : digits(scaledValue);
	const number = new Intl.NumberFormat(locale, { maximumFractionDigits: fraction }).format(
		scaledValue
	);
	return number + NBSP + units[step];
}

/** Bytes in IEC steps of 1024: 1.5 KiB, 12.3 GiB. */
export function formatBytes(bytes: number, locale: Locale): string {
	return scaled(bytes, 1024, IEC, locale);
}

/** Bits per second in SI steps of 1000: 100 Mbit/s. */
export function formatBitsPerSecond(bits: number, locale: Locale): string {
	return scaled(bits, 1000, BITS, locale);
}

/** A share 0..1 as percent: 0.5 %, 42 %. */
export function formatPercent(ratio: number, locale: Locale): string {
	if (!Number.isFinite(ratio)) return '–';
	const fraction = Math.abs(ratio) < 0.1 && ratio !== 0 ? 1 : 0;
	return new Intl.NumberFormat(locale, {
		style: 'percent',
		maximumFractionDigits: fraction
	}).format(ratio);
}

function unit(value: number, name: string, locale: Locale): string {
	return new Intl.NumberFormat(locale, {
		style: 'unit',
		unit: name,
		unitDisplay: 'short',
		maximumFractionDigits: 0
	}).format(value);
}

/** A duration in seconds in its two largest units: 3 days 4 hr, 5 min 3 sec, 4 ms. */
export function formatDuration(seconds: number, locale: Locale): string {
	if (!Number.isFinite(seconds)) return '–';
	if (seconds < 0) return '-' + formatDuration(-seconds, locale);
	if (seconds < 1) {
		return seconds === 0 ? unit(0, 'second', locale) : unit(seconds * 1000, 'millisecond', locale);
	}
	const parts: [number, string][] = [
		[Math.floor(seconds / 86400), 'day'],
		[Math.floor((seconds % 86400) / 3600), 'hour'],
		[Math.floor((seconds % 3600) / 60), 'minute'],
		[Math.floor(seconds % 60), 'second']
	];
	const first = parts.findIndex(([value]) => value > 0);
	return parts
		.slice(first, first + 2)
		.filter(([value], i) => i === 0 || value > 0)
		.map(([value, name]) => unit(value, name, locale))
		.join(' ');
}

/** A point in time given in Unix seconds. */
export function formatTimestamp(seconds: number, locale: Locale): string {
	if (!Number.isFinite(seconds)) return '–';
	return new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(
		new Date(seconds * 1000)
	);
}

export interface FormatOptions {
	/** Show byte rates as bit/s (network views, KONZEPT §17). */
	bits?: boolean;
}

/** Formats a value of the given unit (catalog values: ratios in ppm). */
export function formatValue(
	unitName: Unit,
	value: number,
	locale: Locale,
	options: FormatOptions = {}
): string {
	switch (unitName) {
		case 'ratio':
			return formatPercent(value, locale);
		case 'ratio_ppm':
			return formatPercent(value / 1e6, locale);
		case 'bytes':
			return formatBytes(value, locale);
		case 'bytes_per_second':
			return options.bits
				? formatBitsPerSecond(value * 8, locale)
				: formatBytes(value, locale) + '/s';
		case 'bits_per_second':
			return formatBitsPerSecond(value, locale);
		case 'per_second':
			return formatNumber(value, locale) + NBSP + '/s';
		case 'pages_per_second':
			return m.unit_pages_per_second({ value: formatNumber(value, locale) }, { locale });
		case 'seconds':
			return formatDuration(value, locale);
		case 'timestamp_seconds':
			return formatTimestamp(value, locale);
		case 'celsius':
			return new Intl.NumberFormat(locale, {
				style: 'unit',
				unit: 'celsius',
				maximumFractionDigits: 1
			}).format(value);
		case 'load':
			return new Intl.NumberFormat(locale, {
				minimumFractionDigits: 2,
				maximumFractionDigits: 2
			}).format(value);
		case 'boolean':
			return value > 0 ? m.value_yes({}, { locale }) : m.value_no({}, { locale });
		case 'count':
			return formatNumber(value, locale);
	}
}
