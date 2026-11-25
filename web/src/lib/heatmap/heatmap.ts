/**
 * The fleet heatmap (KONZEPT §16, 7; M4-27): colours, summaries and the range it asks for. Pure,
 * for the page and its tests.
 */

/** A cell without data (the server sends -1). */
export const MISSING = -1;

/** The range the server takes: 10 minutes to 30 days. */
export const MIN_RANGE = 10 * 60_000;
export const MAX_RANGE = 30 * 86_400_000;

/** The range shown: the last 30 days of range, at least 10 minutes up to its end. */
export function heatmapRange(range: { from: number; to: number }): { from: number; to: number } {
	const from = Math.min(Math.max(range.from, range.to - MAX_RANGE), range.to - MIN_RANGE);
	return { from, to: range.to };
}

/**
 * The colour scale, low to high (viridis: even in brightness, readable with colour blindness,
 * in light and dark mode).
 */
export const SCALE = [
	'#440154',
	'#46327e',
	'#365c8d',
	'#277f8e',
	'#1fa187',
	'#4ac16d',
	'#a0da39',
	'#fde725'
] as const;

/** The colour of a value 0..1; undefined for a missing one. */
export function colorOf(value: number): string | undefined {
	if (value < 0 || Number.isNaN(value)) return undefined;
	return SCALE[Math.min(SCALE.length - 1, Math.floor(Math.min(value, 1) * SCALE.length))];
}

/** The mean and the highest value of a row and the column of the highest; undefined without data. */
export interface RowSummary {
	mean: number;
	max: number;
	maxAt: number;
}

export function summarize(values: number[]): RowSummary | undefined {
	let sum = 0;
	let n = 0;
	let max = -Infinity;
	let maxAt = -1;
	values.forEach((v, i) => {
		if (v < 0 || Number.isNaN(v)) return;
		sum += v;
		n++;
		if (v > max) {
			max = v;
			maxAt = i;
		}
	});
	return n === 0 ? undefined : { mean: sum / n, max, maxAt };
}

export type RowOrder = 'name' | 'max' | 'mean';

/** Rows in the chosen order: by host name, or the highest first (rows without data last). */
export function orderRows<T extends { hostname: string; values: number[] }>(
	rows: T[],
	order: RowOrder
): T[] {
	if (order === 'name') return [...rows].sort((a, b) => a.hostname.localeCompare(b.hostname));
	const key = (r: T) => summarize(r.values)?.[order] ?? -1;
	return [...rows].sort((a, b) => key(b) - key(a) || a.hostname.localeCompare(b.hostname));
}

/** Moves a cursor (row, column) by a key; undefined for any other key. */
export function moveCursor(
	key: string,
	cursor: { row: number; column: number },
	rows: number,
	columns: number
): { row: number; column: number } | undefined {
	const clamp = (v: number, n: number) => Math.min(Math.max(v, 0), n - 1);
	const { row, column } = cursor;
	switch (key) {
		case 'ArrowLeft':
			return { row, column: clamp(column - 1, columns) };
		case 'ArrowRight':
			return { row, column: clamp(column + 1, columns) };
		case 'ArrowUp':
			return { row: clamp(row - 1, rows), column };
		case 'ArrowDown':
			return { row: clamp(row + 1, rows), column };
		case 'Home':
			return { row, column: 0 };
		case 'End':
			return { row, column: columns - 1 };
		case 'PageUp':
			return { row: clamp(row - 10, rows), column };
		case 'PageDown':
			return { row: clamp(row + 10, rows), column };
	}
	return undefined;
}
