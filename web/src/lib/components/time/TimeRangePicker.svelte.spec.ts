import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { timeRange } from '#lib/stores/timerange.svelte.js';
import TimeRangePicker from './TimeRangePicker.svelte';

describe('TimeRangePicker', () => {
	beforeEach(() => {
		switchLocale('en');
		timeRange.setPreset('1h');
	});
	afterEach(() => timeRange.setPreset('1h'));

	it('picks a preset', async () => {
		render(TimeRangePicker);

		await page.getByRole('button', { name: '6 h' }).click();

		expect(timeRange.preset).toBe('6h');
		await expect
			.element(page.getByRole('button', { name: '6 h' }))
			.toHaveAttribute('aria-pressed', 'true');
		await expect.element(page.getByRole('group', { name: 'Time range' })).toBeInTheDocument();
	});

	it('offers to reset a zoom', async () => {
		render(TimeRangePicker);
		const now = Date.now();

		timeRange.zoom(now - 1800_000, now - 600_000);

		await expect
			.element(page.getByRole('button', { name: '1 h' }))
			.toHaveAttribute('aria-pressed', 'false');
		await page.getByRole('button', { name: 'Reset zoom' }).click();
		expect(timeRange.live).toBe(true);
		expect(page.getByRole('button', { name: 'Reset zoom' }).query()).toBeNull();
	});
});
