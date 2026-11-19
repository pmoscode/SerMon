import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { helpState } from '#lib/stores/help.svelte.js';
import HelpPanel from './HelpPanel.svelte';
import HelpTip from './HelpTip.svelte';

describe('HelpTip and HelpPanel', () => {
	beforeEach(() => switchLocale('en'));
	afterEach(() => helpState.close());

	it('shows the summary and opens the full text in the panel', async () => {
		render(HelpTip, { topic: 'pages/fleet' });
		render(HelpPanel);

		await page.getByRole('button', { name: 'Help: Fleet' }).click();
		await expect.element(page.getByText(/^The overview of all servers/)).toBeVisible();

		await page.getByRole('button', { name: 'Learn more' }).click();

		const panel = page.getByRole('dialog');
		await expect.element(panel.getByRole('heading', { name: 'Reading this page' })).toBeVisible();
		await panel.getByRole('button', { name: 'CPU busy' }).click();
		await expect.element(panel.getByRole('heading', { name: 'CPU busy', level: 2 })).toBeVisible();

		await panel.getByRole('button', { name: 'Close help' }).click();
		expect(helpState.topic).toBeUndefined();
	});

	it('follows the UI language', async () => {
		switchLocale('de');
		render(HelpTip, { topic: 'pages/fleet' });

		await expect.element(page.getByRole('button', { name: 'Hilfe: Flotte' })).toBeVisible();
	});

	it('renders markup in help texts as text', async () => {
		await helpState.load('en');
		helpState.docs = {
			...helpState.docs,
			'pages/evil': {
				title: '<img src=x onerror=alert(1)>',
				summary: [{ type: 'text', text: '<script>alert(1)</script>' }],
				body: []
			}
		};
		render(HelpPanel);

		helpState.open('pages/evil');

		await expect.element(page.getByText('<script>alert(1)</script>')).toBeVisible();
		expect(document.querySelector('[role="dialog"] img, [role="dialog"] script')).toBeNull();
	});

	it('says so when a topic has no help yet', async () => {
		render(HelpPanel);

		helpState.open('metrics/sermon_unknown');

		await expect.element(page.getByText('There is no help for this topic yet.')).toBeVisible();
	});
});
