import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { m } from '#lib/paraglide/messages.js';
import { timeRange } from '#lib/stores/timerange.svelte.js';
import TimeSeries from './TimeSeries.svelte';

const now = Date.now();
const minute = 60_000;
const series = [
	{
		label: 'web-01',
		points: [50, 40, 30, 20, 10].map((ago, i) => ({
			time: now - ago * minute,
			value: (i + 1) * 100_000
		}))
	}
];

function drag(over: HTMLElement, fromX: number, toX: number) {
	const box = over.getBoundingClientRect();
	const at = (x: number) => ({
		clientX: box.left + x,
		clientY: box.top + box.height / 2,
		bubbles: true
	});
	over.dispatchEvent(new MouseEvent('mousemove', at(fromX)));
	over.dispatchEvent(new MouseEvent('mousedown', { ...at(fromX), button: 0 }));
	over.dispatchEvent(new MouseEvent('mousemove', { ...at(toX), movementX: toX - fromX }));
	document.dispatchEvent(new MouseEvent('mouseup', { ...at(toX), button: 0 }));
}

describe('TimeSeries', () => {
	beforeEach(() => {
		switchLocale('en');
		timeRange.setPreset('1h');
	});
	afterEach(() => timeRange.reset());

	it('draws the series and summarizes it for screen readers', async () => {
		render(TimeSeries, { title: 'CPU busy', unit: 'ratio_ppm', series });

		await expect.element(page.getByRole('figure', { name: 'CPU busy' })).toBeVisible();
		expect(document.querySelector('canvas')).not.toBeNull();
		await expect
			.element(page.getByText('web-01: minimum 10%, maximum 50%, latest 50%'))
			.toBeInTheDocument();
	});

	it('follows the global time range, also when zoomed elsewhere', async () => {
		render(TimeSeries, { title: 'CPU busy', unit: 'ratio_ppm', series });

		timeRange.zoom(now - 55 * minute, now - 35 * minute);

		await expect
			.element(page.getByText('web-01: minimum 10%, maximum 20%, latest 20%'))
			.toBeInTheDocument();
	});

	it('zooms all charts on a drag selection and resets on double click', async () => {
		render(TimeSeries, { title: 'CPU busy', unit: 'ratio_ppm', series });
		await expect.element(page.getByRole('figure', { name: 'CPU busy' })).toBeVisible();
		const over = document.querySelector<HTMLElement>('.u-over')!;

		drag(over, 10, over.clientWidth / 2);

		expect(timeRange.live).toBe(false);
		const span = timeRange.range.to - timeRange.range.from;
		expect(span).toBeGreaterThan(20 * minute);
		expect(span).toBeLessThan(35 * minute);

		over.dispatchEvent(new MouseEvent('dblclick', { bubbles: true }));
		expect(timeRange.live).toBe(true);
	});

	it('shows loading, error and empty states', async () => {
		const { rerender } = await render(TimeSeries, {
			title: 'Load',
			unit: 'load',
			series: [],
			loading: true
		});
		await expect.element(page.getByRole('status')).toHaveTextContent(m.loading());

		await rerender({ loading: false, error: 'The server can’t be reached.' });
		await expect.element(page.getByRole('alert')).toHaveTextContent('The server can’t be reached.');

		await rerender({ error: '' });
		await expect.element(page.getByText('No data in the selected time range.')).toBeVisible();
	});
});
