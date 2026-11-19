import AxeBuilder from '@axe-core/playwright';
import { installed, mockApi } from './api';
import { expect, test } from './fixtures';

test.describe('start page (served by sermon-server)', () => {
	test.beforeEach(async ({ page }) => {
		await mockApi(page, installed(false));
	});

	test('sends anonymous visitors to the login in English by default', async ({ page }) => {
		await page.goto('/');

		await expect(page).toHaveURL(/\/login$/);
		await expect(page).toHaveTitle('Sign in · SerMon');
		await expect(page.getByRole('heading', { level: 1 })).toHaveText('Sign in');
		await expect(page.locator('html')).toHaveAttribute('lang', 'en');
	});

	test('has no accessibility violations', async ({ page }) => {
		await page.goto('/login');
		await expect(page.getByRole('heading', { level: 1 })).toBeVisible();

		const results = await new AxeBuilder({ page }).analyze();

		expect(results.violations).toEqual([]);
	});

	test('remembers the chosen language across reloads', async ({ page }) => {
		await page.goto('/login');
		await page.getByRole('button', { name: 'Deutsch' }).click();
		await expect(page.getByRole('heading', { level: 1 })).toHaveText('Anmelden');

		await page.reload();

		await expect(page.getByRole('heading', { level: 1 })).toHaveText('Anmelden');
		await expect(page.locator('html')).toHaveAttribute('lang', 'de');
	});

	test('uses the bundled fonts served by sermon-server', async ({ page }) => {
		await page.goto('/login');
		await expect(page.getByRole('heading', { level: 1 })).toBeVisible();

		const loaded = await page.evaluate(async () => {
			await document.fonts.ready;
			return [...document.fonts].filter((f) => f.status === 'loaded').map((f) => f.family);
		});

		expect(loaded.some((family) => family.includes('Inter'))).toBe(true);
	});

	test('sends a strict Content-Security-Policy', async ({ page }) => {
		const response = await page.goto('/login');
		const csp = response?.headers()['content-security-policy'] ?? '';

		expect(csp).toContain("default-src 'self'");
		expect(csp).toContain("script-src 'self' 'sha256-");
		expect(csp).not.toContain('unsafe-inline');
		expect(csp).not.toMatch(/https?:/);
	});

	test('serves client-side routes (deep links) via the SPA fallback', async ({ page }) => {
		const response = await page.goto('/nodes/42');

		expect(response?.status()).toBe(200);
		expect(response?.headers()['content-type']).toContain('text/html');
	});
});

test.describe('with a German browser', () => {
	test.use({ locale: 'de-DE' });

	test('picks German from the browser language', async ({ page }) => {
		await mockApi(page, installed(false));
		await page.goto('/');

		await expect(page.getByRole('heading', { level: 1 })).toHaveText('Anmelden');
	});
});
