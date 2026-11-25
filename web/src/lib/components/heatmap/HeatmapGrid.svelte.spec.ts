import { beforeEach, describe, expect, it } from 'vitest';
import { page, userEvent } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import HeatmapGrid from './HeatmapGrid.svelte';

const start = Date.UTC(2026, 10, 19, 12, 0);
/** The line that names the cell. */
const status = () => expect.poll(() => page.getByRole('status').element().textContent?.trim());
const rows = [
	{ nodeId: 'a', hostname: 'web-01', values: [0.1, 0.5, -1] },
	{ nodeId: 'b', hostname: 'db-01', values: [0.9, 0.25, 0.75] }
];

describe('HeatmapGrid', () => {
	beforeEach(() => switchLocale('en'));

	it('names the cell under the keyboard cursor', async () => {
		render(HeatmapGrid, { rows, start, step: 600_000, columns: 3, label: 'CPU' });
		const grid = page.getByRole('application', { name: /CPU of 2 nodes over time/ });
		await status().toMatch(/arrow keys/);
		(grid.element() as HTMLElement).focus();
		await userEvent.keyboard('{ArrowLeft}{ArrowLeft}');
		await status().toMatch(/^web-01 · .+ – .+ · 10%$/);
		await userEvent.keyboard('{ArrowRight}');
		await status().toMatch(/web-01 .* 50%$/);
		await userEvent.keyboard('{ArrowRight}');
		await status().toMatch(/web-01 .* no data$/);
		await userEvent.keyboard('{ArrowDown}{Home}');
		await status().toMatch(/db-01 .* 90%$/);
		await userEvent.keyboard('{End}');
		await status().toMatch(/db-01 .* 75%$/);
	});

	it('names the cell under the pointer', async () => {
		render(HeatmapGrid, { rows, start, step: 600_000, columns: 3, label: 'CPU' });
		const grid = page.getByRole('application');
		const box = grid.element().getBoundingClientRect();
		// The second row (16 px each), the middle of the three columns.
		await grid.hover({ position: { x: box.width / 2, y: 20 } });
		await status().toMatch(/^db-01 · .+ · 25%$/);
		await grid.hover({ position: { x: 2, y: 4 } });
		await status().toMatch(/^web-01 · .+ · 10%$/);
	});

	it('starts at the latest column of the first node on focus', async () => {
		render(HeatmapGrid, { rows, start, step: 600_000, columns: 3, label: 'Memory' });
		await userEvent.tab();
		await status().toMatch(/web-01 .* no data$/);
	});
});
