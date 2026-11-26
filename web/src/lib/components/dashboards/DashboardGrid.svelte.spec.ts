import { createRawSnippet } from 'svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { page, userEvent } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import type { Item } from '#lib/dashboards/layout.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import DashboardGrid from './DashboardGrid.svelte';

const widget = createRawSnippet((id: () => string) => ({
	render: () => `<p>content of ${id()}</p>`
}));
const titles = { cpu: 'CPU', mem: 'Memory' };
const start: Item[] = [
	{ id: 'cpu', x: 0, y: 0, w: 6, h: 4 },
	{ id: 'mem', x: 6, y: 0, w: 6, h: 4 }
];
const status = () => expect.poll(() => page.getByRole('status').element().textContent?.trim());

/** Renders an editable grid that follows its own changes like the editor does. */
async function editor() {
	let items = start;
	const changes: Item[][] = [];
	const onremove = vi.fn();
	const screen = await render(DashboardGrid, {
		items,
		titles,
		label: 'Widgets',
		editable: true,
		widget,
		onremove,
		onchange: (next: Item[]) => {
			changes.push(next);
			items = next;
			void screen.rerender({ items });
		}
	});
	return { changes, onremove, last: () => changes.at(-1)! };
}

function box(items: Item[], id: string) {
	return items.find((i) => i.id === id);
}

describe('DashboardGrid', () => {
	beforeEach(() => switchLocale('en'));

	it('shows the widgets without handles outside the editor', async () => {
		render(DashboardGrid, { items: start, titles, label: 'Widgets', widget });
		const list = page.getByRole('list', { name: 'Widgets' });
		await expect.element(list.getByRole('listitem', { name: 'CPU' })).toBeVisible();
		await expect.element(page.getByText('content of mem')).toBeVisible();
		expect(page.getByRole('button').elements()).toHaveLength(0);
	});

	it('moves and resizes by keyboard and says where the widget is', async () => {
		const { last, onremove } = await editor();
		const handle = page.getByRole('button', { name: 'Move or resize CPU' });
		(handle.element() as HTMLElement).focus();
		await userEvent.keyboard('{ArrowDown}');
		expect(box(last(), 'cpu')).toMatchObject({ x: 0, y: 1, w: 6, h: 4 });
		await status().toBe('CPU: column 1, row 2, 6 × 4');
		await userEvent.keyboard('{Shift>}{ArrowDown}{/Shift}');
		expect(box(last(), 'cpu')).toMatchObject({ y: 1, h: 5 });
		await userEvent.keyboard('{Shift>}{ArrowLeft}{/Shift}');
		expect(box(last(), 'cpu')).toMatchObject({ w: 5 });
		// Moving right into Memory pushes it down.
		await userEvent.keyboard('{ArrowRight}{ArrowRight}');
		expect(box(last(), 'cpu')).toMatchObject({ x: 2 });
		expect(box(last(), 'mem')).toMatchObject({ x: 6, y: 6 });
		await status().toBe('CPU: column 3, row 2, 5 × 5');
		// The handle keeps the focus while the widget moves.
		expect(document.activeElement).toBe(handle.element());
		await userEvent.keyboard('{Delete}');
		expect(onremove).toHaveBeenCalledWith('cpu');
	});

	it('names the place when the widget is at the edge', async () => {
		const { changes } = await editor();
		(page.getByRole('button', { name: 'Move or resize CPU' }).element() as HTMLElement).focus();
		await userEvent.keyboard('{ArrowLeft}{ArrowUp}');
		expect(changes).toHaveLength(0);
		await status().toBe('CPU: column 1, row 1, 6 × 4');
	});

	it('moves by dragging the handle and resizes at the corner, in whole cells', async () => {
		const { last } = await editor();
		const grid = page.getByRole('list', { name: 'Widgets' }).element() as HTMLElement;
		const cell = (grid.clientWidth + 12) / 12;
		const handle = page.getByRole('button', { name: 'Move or resize Memory' }).element();
		const pointer = (el: Element, type: string, x: number, y: number) =>
			el.dispatchEvent(
				new PointerEvent(type, { bubbles: true, button: 0, pointerId: 1, clientX: x, clientY: y })
			);
		pointer(handle, 'pointerdown', 100, 100);
		pointer(handle, 'pointermove', 100 - 2 * cell, 100 + 76 * 2);
		pointer(handle, 'pointerup', 100 - 2 * cell, 100 + 76 * 2);
		await expect.poll(() => box(last(), 'mem')).toMatchObject({ x: 4, y: 2, w: 6, h: 4 });
		// Memory now covers the right part of CPU: CPU is pushed below it.
		expect(box(last(), 'cpu')).toMatchObject({ x: 0, y: 6 });

		const corner = grid.querySelector('[data-resize="mem"]')!;
		pointer(corner, 'pointerdown', 0, 0);
		pointer(corner, 'pointermove', cell, 76);
		pointer(corner, 'pointerup', cell, 76);
		await expect.poll(() => box(last(), 'mem')).toMatchObject({ x: 4, y: 2, w: 7, h: 5 });
	});

	it('drops a drag that is cancelled', async () => {
		const { changes } = await editor();
		const handle = page.getByRole('button', { name: 'Move or resize CPU' }).element();
		const pointer = (type: string, x: number) =>
			handle.dispatchEvent(
				new PointerEvent(type, { bubbles: true, button: 0, pointerId: 1, clientX: x, clientY: 0 })
			);
		pointer('pointerdown', 0);
		pointer('pointermove', 400);
		pointer('pointercancel', 400);
		await new Promise((r) => requestAnimationFrame(r));
		expect(changes).toHaveLength(0);
	});
});
