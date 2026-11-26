import { describe, expect, it } from 'vitest';
import { clamp, firstFree, height, nudge, overlaps, place, ROWS, type Item } from './layout.js';

const a: Item = { id: 'a', x: 0, y: 0, w: 6, h: 4 };
const b: Item = { id: 'b', x: 6, y: 0, w: 6, h: 4 };
const c: Item = { id: 'c', x: 0, y: 4, w: 12, h: 2 };

function noOverlaps(items: Item[]) {
	for (const [i, x] of items.entries())
		for (const y of items.slice(i + 1)) expect(overlaps(x, y), `${x.id} / ${y.id}`).toBe(false);
}

describe('layout', () => {
	it('lets widgets touch without overlapping', () => {
		expect(overlaps(a, b)).toBe(false);
		expect(overlaps(b, a)).toBe(false);
		expect(overlaps(a, c)).toBe(false);
		expect(overlaps(a, { x: 5, y: 3, w: 2, h: 2 })).toBe(true);
	});

	it('keeps boxes within the grid', () => {
		expect(clamp({ x: 10, y: -2, w: 6, h: 20 })).toEqual({ x: 6, y: 0, w: 6, h: 12 });
		expect(clamp({ x: -1, y: 99, w: 0, h: 4 })).toEqual({ x: 0, y: 96, w: 1, h: 4 });
		expect(clamp({ x: 0, y: 0, w: 14, h: 1 })).toEqual({ x: 0, y: 0, w: 12, h: 1 });
	});

	it('pushes the widgets a moved one covers downwards, in a cascade', () => {
		const out = place([a, b, c], 'a', { x: 3, y: 0, w: 6, h: 4 });
		expect(out).toBeDefined();
		const byId = Object.fromEntries(out!.map((i) => [i.id, i]));
		expect(byId.a).toMatchObject({ x: 3, y: 0 });
		expect(byId.b).toMatchObject({ x: 6, y: 4 });
		// c was under b's new place: pushed below it.
		expect(byId.c).toMatchObject({ x: 0, y: 8 });
		noOverlaps(out!);
	});

	it('leaves widgets it does not touch where they are and does not change the input', () => {
		const out = place([a, b, c], 'c', { x: 0, y: 6, w: 12, h: 2 });
		expect(out).toEqual([a, b, { ...c, y: 6 }]);
		expect(c.y).toBe(4);
	});

	it('refuses a move that would push a widget past the last row', () => {
		const low: Item = { id: 'low', x: 0, y: ROWS - 4, w: 12, h: 4 };
		const top: Item = { id: 'top', x: 0, y: ROWS - 8, w: 12, h: 4 };
		expect(place([top, low], 'top', { x: 0, y: ROWS - 6, w: 12, h: 4 })).toBeUndefined();
		expect(place([a], 'nope', { x: 0, y: 0, w: 1, h: 1 })).toBeUndefined();
	});

	it('moves and resizes by keyboard', () => {
		expect(nudge([a, b], 'a', 'ArrowDown', false)).toEqual([{ ...a, y: 1 }, b]);
		// Right pushes b down instead of overlapping it.
		const right = nudge([a, b], 'a', 'ArrowRight', false)!;
		expect(right[0]).toMatchObject({ x: 1, y: 0 });
		expect(right[1]).toMatchObject({ x: 6, y: 4 });
		expect(nudge([a, b], 'a', 'ArrowDown', true)).toEqual([{ ...a, h: 5 }, b]);
		expect(nudge([a, b], 'a', 'ArrowLeft', true)).toEqual([{ ...a, w: 5 }, b]);
		expect(nudge([a, b], 'a', 'ArrowUp', true)).toEqual([{ ...a, h: 3 }, b]);
	});

	it('does nothing at the edges', () => {
		expect(nudge([a], 'a', 'ArrowLeft', false)).toBeUndefined();
		expect(nudge([a], 'a', 'ArrowUp', false)).toBeUndefined();
		expect(nudge([{ ...a, w: 1, h: 1 }], 'a', 'ArrowLeft', true)).toBeUndefined();
		expect(nudge([{ ...a, x: 0, w: 12 }], 'a', 'ArrowRight', true)).toBeUndefined();
		expect(nudge([{ ...a, h: 12 }], 'a', 'ArrowDown', true)).toBeUndefined();
	});

	it('finds the first free place for a new widget', () => {
		expect(firstFree([], 6, 4)).toEqual({ x: 0, y: 0, w: 6, h: 4 });
		expect(firstFree([a], 6, 4)).toEqual({ x: 6, y: 0, w: 6, h: 4 });
		expect(firstFree([a, b, c], 4, 3)).toEqual({ x: 0, y: 6, w: 4, h: 3 });
		const full = Array.from({ length: ROWS }, (_, y) => ({ x: 0, y, w: 12, h: 1 }));
		expect(firstFree(full, 1, 1)).toBeUndefined();
	});

	it('knows how many rows are used', () => {
		expect(height([])).toBe(1);
		expect(height([a, b, c])).toBe(6);
	});
});
