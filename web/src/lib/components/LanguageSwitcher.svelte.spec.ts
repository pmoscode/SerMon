import { beforeEach, describe, expect, it } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { localeState, switchLocale } from '#lib/i18n/locale.svelte.js';
import LanguageSwitcher from './LanguageSwitcher.svelte';

describe('LanguageSwitcher', () => {
	beforeEach(() => switchLocale('en'));

	it('offers every language with its own name and marks the active one', async () => {
		render(LanguageSwitcher);

		await expect
			.element(page.getByRole('button', { name: 'English' }))
			.toHaveAttribute('aria-pressed', 'true');
		await expect
			.element(page.getByRole('button', { name: 'Deutsch' }))
			.toHaveAttribute('aria-pressed', 'false');
	});

	it('switches the language on click', async () => {
		render(LanguageSwitcher);

		await page.getByRole('button', { name: 'Deutsch' }).click();

		expect(localeState.current).toBe('de');
		await expect
			.element(page.getByRole('button', { name: 'Deutsch' }))
			.toHaveAttribute('aria-pressed', 'true');
	});

	it('has an accessible group label in the current language', async () => {
		switchLocale('de');
		render(LanguageSwitcher);

		await expect.element(page.getByRole('group', { name: 'Sprache' })).toBeInTheDocument();
	});
});
