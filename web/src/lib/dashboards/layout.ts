/**
 * The grid of the custom dashboards (ADR 0040): 12 columns, at most 100 rows, a widget at most
 * 12 rows high, no overlaps (the server checks the same). Moving or resizing a widget pushes the
 * widgets it would cover downwards; nothing floats up on its own, so a widget stays where it was put.
 */

export const COLUMNS = 12;
export const ROWS = 100;
export const MAX_HEIGHT = 12;

export interface Box {
	x: number;
	y: number;
	w: number;
	h: number;
}

export interface Item extends Box {
	id: string;
}

export function overlaps(a: Box, b: Box): boolean {
	return a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h;
}

/** The box within the grid: at least one cell, at most the columns and MAX_HEIGHT rows. */
export function clamp(box: Box): Box {
	const w = Math.min(Math.max(Math.round(box.w), 1), COLUMNS);
	const h = Math.min(Math.max(Math.round(box.h), 1), MAX_HEIGHT);
	const x = Math.min(Math.max(Math.round(box.x), 0), COLUMNS - w);
	const y = Math.min(Math.max(Math.round(box.y), 0), ROWS - h);
	return { x, y, w, h };
}

/**
 * Puts the item `id` at `box` (clamped) and pushes every widget it covers below it, and so on.
 * Returns the new layout, or undefined if a widget would leave the last row (the move is refused).
 */
export function place(items: Item[], id: string, box: Box): Item[] | undefined {
	const target = items.find((i) => i.id === id);
	if (!target) return undefined;
	const moved: Item = { id, ...clamp(box) };
	const out = items.map((i) => (i.id === id ? moved : { ...i }));
	// Settle from top to bottom: each widget is pushed below the ones already settled it covers.
	const settled: Item[] = [moved];
	const rest = out
		.filter((i) => i.id !== id)
		.sort((a, b) => a.y - b.y || a.x - b.x || a.id.localeCompare(b.id));
	for (const item of rest) {
		let hit = settled.find((s) => overlaps(s, item));
		while (hit) {
			item.y = hit.y + hit.h;
			if (item.y + item.h > ROWS) return undefined;
			hit = settled.find((s) => overlaps(s, item));
		}
		settled.push(item);
	}
	return out;
}

/** The first free place for a new widget of w × h, row by row from the top; undefined if full. */
export function firstFree(items: Box[], w: number, h: number): Box | undefined {
	const size = clamp({ x: 0, y: 0, w, h });
	for (let y = 0; y + size.h <= ROWS; y++) {
		for (let x = 0; x + size.w <= COLUMNS; x++) {
			const box = { x, y, w: size.w, h: size.h };
			if (!items.some((i) => overlaps(i, box))) return box;
		}
	}
	return undefined;
}

/** The rows the layout uses (at least one). */
export function height(items: Box[]): number {
	return Math.max(1, ...items.map((i) => i.y + i.h));
}

export type Key = 'ArrowLeft' | 'ArrowRight' | 'ArrowUp' | 'ArrowDown';

/**
 * The keyboard of the editor: arrow keys move by one cell, with Shift they change the size
 * (right and down grow, left and up shrink). Returns the new layout or undefined if nothing moves.
 */
export function nudge(items: Item[], id: string, key: Key, resize: boolean): Item[] | undefined {
	const item = items.find((i) => i.id === id);
	if (!item) return undefined;
	const dx = key === 'ArrowLeft' ? -1 : key === 'ArrowRight' ? 1 : 0;
	const dy = key === 'ArrowUp' ? -1 : key === 'ArrowDown' ? 1 : 0;
	const box = resize
		? { x: item.x, y: item.y, w: item.w + dx, h: item.h + dy }
		: { x: item.x + dx, y: item.y + dy, w: item.w, h: item.h };
	const next = clamp(box);
	if (next.x === item.x && next.y === item.y && next.w === item.w && next.h === item.h)
		return undefined;
	return place(items, id, next);
}
