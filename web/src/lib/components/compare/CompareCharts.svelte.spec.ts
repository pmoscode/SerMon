import { beforeEach, describe, expect, it } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { timeRange } from '#lib/stores/timerange.svelte.js';
import type { CompareResult } from '#lib/compare/compare.svelte.js';
import { compareMetrics } from '#lib/compare/compare.js';
import CompareCharts from './CompareCharts.svelte';

const a = '0193f5a0-0000-7000-8000-000000000001';
const b = '0193f5a0-0000-7000-8000-000000000002';
const c = '0193f5a0-0000-7000-8000-000000000003';
const nodes = [
	{ id: a, hostname: 'web-01' },
	{ id: b, hostname: 'web-02' },
	{ id: c, hostname: 'db-01' }
];
const now = Date.now();
const points = (value: number) => [
	{ time: now - 20 * 60_000, value },
	{ time: now - 10 * 60_000, value }
];

/** CPU of web-01 at 20 %, web-02 at 60 %, db-01 without data; the rest empty. */
function results(error = false): Record<string, CompareResult> {
	const out: Record<string, CompareResult> = {};
	for (const metric of compareMetrics)
		out[metric.key] = {
			perNode:
				metric.key === 'cpu'
					? new Map([
							[a, points(200_000)],
							[b, points(600_000)]
						])
					: new Map(),
			error: metric.key === 'memory' && error
		};
	return out;
}

describe('CompareCharts', () => {
	beforeEach(() => {
		switchLocale('en');
		timeRange.setPreset('1h');
	});

	it('overlays the nodes in one chart per metric', async () => {
		render(CompareCharts, { nodes, view: 'overlay', results: results(), loading: false });
		const cpu = page.getByRole('figure', { name: 'CPU busy', exact: true });
		await expect.element(cpu).toBeVisible();
		const text = cpu.element().textContent ?? '';
		expect(text).toContain('web-01: minimum 20%, maximum 20%, latest 20%');
		expect(text).toContain('web-02: minimum 60%, maximum 60%, latest 60%');
		expect(page.getByRole('figure').elements()).toHaveLength(compareMetrics.length);
	});

	it('puts one chart per node side by side', async () => {
		render(CompareCharts, { nodes, view: 'side', results: results(), loading: false });
		await expect.element(page.getByRole('figure', { name: 'CPU busy · web-02' })).toBeVisible();
		expect(page.getByRole('figure').elements()).toHaveLength(compareMetrics.length * nodes.length);
		const db = page.getByRole('figure', { name: 'CPU busy · db-01' });
		expect(db.element().textContent).toContain('No data in the selected time range.');
	});

	it('names the nodes without data in the range', async () => {
		render(CompareCharts, { nodes, view: 'overlay', results: results(), loading: false });
		const note = page.getByRole('note');
		await expect.element(note).toBeVisible();
		expect(note.element().textContent).toContain('db-01');
		expect(note.element().textContent).not.toContain('web-01');
	});

	it('says nothing about missing data while loading', async () => {
		render(CompareCharts, { nodes, view: 'overlay', results: {}, loading: true });
		await expect.element(page.getByRole('figure', { name: 'CPU busy', exact: true })).toBeVisible();
		expect(page.getByRole('note').elements()).toHaveLength(0);
	});

	it('shows a failed metric as an error and the others as usual', async () => {
		render(CompareCharts, { nodes, view: 'overlay', results: results(true), loading: false });
		const memory = page.getByRole('figure', { name: 'Memory used', exact: true });
		await expect.element(memory.getByRole('alert')).toBeVisible();
		const cpu = page.getByRole('figure', { name: 'CPU busy', exact: true });
		expect(cpu.getByRole('alert').elements()).toHaveLength(0);
	});
});
