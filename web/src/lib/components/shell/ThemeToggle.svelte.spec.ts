import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { themeState } from '#lib/theme/theme.svelte.js';
import ThemeToggle from './ThemeToggle.svelte';

describe('ThemeToggle', () => {
	beforeEach(() => switchLocale('en'));
	afterEach(() => themeState.set('system'));

	it('switches to dark and marks the choice', async () => {
		render(ThemeToggle);

		await page.getByRole('button', { name: 'Dark' }).click();

		await expect
			.element(page.getByRole('button', { name: 'Dark' }))
			.toHaveAttribute('aria-pressed', 'true');
		await expect
			.element(page.getByRole('button', { name: 'System' }))
			.toHaveAttribute('aria-pressed', 'false');
		expect(document.documentElement.classList.contains('dark')).toBe(true);
		await expect.element(page.getByRole('group', { name: 'Appearance' })).toBeInTheDocument();
	});
});
