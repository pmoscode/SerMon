import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page, userEvent } from 'vitest/browser';
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

	// M2-34: the popover loads on the first opening; the button toggles it, Escape closes it and
	// the focus goes back to the button.
	it('opens on demand, toggles and returns the focus', async () => {
		render(HelpTip, { topic: 'pages/fleet' });
		const button = page.getByRole('button', { name: 'Help: Fleet' });
		await expect.element(button).toHaveAttribute('aria-expanded', 'false');
		await button.click();
		await expect.element(page.getByText(/^The overview of all servers/)).toBeVisible();
		await expect.element(button).toHaveAttribute('aria-expanded', 'true');
		await button.click();
		await expect.element(page.getByText(/^The overview of all servers/)).not.toBeInTheDocument();
		await button.click();
		await expect.element(page.getByText(/^The overview of all servers/)).toBeVisible();
		await userEvent.keyboard('{Escape}');
		await expect.element(page.getByText(/^The overview of all servers/)).not.toBeInTheDocument();
		await expect.element(button).toHaveFocus();
	});

	// The tooltips of every page need only the summaries; the full texts load with the panel.
	it('loads the full texts only for the panel', async () => {
		switchLocale('de');
		render(HelpTip, { topic: 'pages/alerts' });
		await expect.element(page.getByRole('button', { name: 'Hilfe: Alerts' })).toBeVisible();
		await vi.waitFor(() => expect(helpState.summaries).toBe('de'));
		expect(helpState.locale).toBeUndefined();
		expect(helpState.get('pages/alerts')?.body).toEqual([]);
		expect(helpState.get('pages/alerts')?.more).toBe(true);
		render(HelpPanel);
		helpState.open('pages/alerts');
		await expect.element(page.getByRole('heading', { name: 'Quittieren' })).toBeVisible();
		expect(helpState.locale).toBe('de');
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
