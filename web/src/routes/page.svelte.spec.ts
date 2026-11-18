import { beforeEach, describe, expect, it } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import Page from './+page.svelte';

describe('start page', () => {
	beforeEach(() => switchLocale('en'));

	it('greets in English by default', async () => {
		render(Page);

		await expect
			.element(page.getByRole('heading', { level: 1 }))
			.toHaveTextContent('Welcome to SerMon');
	});

	it('greets in German after switching the language', async () => {
		render(Page);

		await page.getByRole('button', { name: 'Deutsch' }).click();

		await expect
			.element(page.getByRole('heading', { level: 1 }))
			.toHaveTextContent('Willkommen bei SerMon');
	});
});
