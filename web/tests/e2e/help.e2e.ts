import AxeBuilder from '@axe-core/playwright';
import { installed, mockApi } from './api';
import { expect, test } from './fixtures';

test.describe('context help (mocked API)', () => {
	test.beforeEach(async ({ page }) => {
		await mockApi(page, installed(true));
		await page.goto('/fleet');
		await expect(page.getByRole('heading', { level: 1 })).toHaveText('Fleet');
	});

	test('the ⓘ shows the summary, "Learn more" the panel', async ({ page }) => {
		await page.getByRole('button', { name: 'Help: Fleet' }).click();
		await expect(page.getByText(/^The overview of all servers/)).toBeVisible();

		await page.getByRole('button', { name: 'Learn more' }).click();

		const panel = page.getByRole('dialog');
		await expect(panel.getByRole('heading', { name: 'Reading this page' })).toBeVisible();
		const results = await new AxeBuilder({ page }).analyze();
		expect(results.violations).toEqual([]);
	});

	test('"?" and the help button open the help of the page', async ({ page }) => {
		await page.keyboard.press('?');

		const panel = page.getByRole('dialog');
		await expect(panel.getByRole('heading', { name: 'Fleet', level: 2 })).toBeVisible();
		await panel.getByRole('button', { name: 'CPU busy' }).click();
		await expect(panel.getByRole('heading', { name: 'CPU busy', level: 2 })).toBeVisible();

		await page.keyboard.press('Escape');
		await expect(panel).toBeHidden();

		await page.getByRole('button', { name: 'Help for this page' }).click();
		await expect(page.getByRole('dialog')).toBeVisible();
	});

	test('shows the help in German', async ({ page }) => {
		await page.getByRole('button', { name: 'Deutsch' }).click();
		await page.getByRole('button', { name: 'Hilfe zu dieser Seite' }).click();

		await expect(
			page.getByRole('dialog').getByRole('heading', { name: 'So liest du die Seite' })
		).toBeVisible();
	});
});
