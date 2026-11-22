import AxeBuilder from '@axe-core/playwright';
import { installed, mockApi, session } from './api';
import { expect, test } from './fixtures';

/** An admin sees every menu item; German has the longest labels. */
const admin = { ...session, orgAdmin: true, enrollGroupIds: ['*'], silenceGroupIds: ['*'] };

test.describe('app shell (mocked API)', () => {
	// M2-23: with "Notifications" the header wrapped at 1280 px; theme, language and sign-out now
	// live in the account menu.
	test('keeps the header on one line at 1280 px in English and German', async ({ page }) => {
		await page.setViewportSize({ width: 1280, height: 800 });
		await mockApi(page, {
			...installed(true),
			'AuthService/GetSession': () => ({ json: { session: admin } }),
			'FleetService/GetFleetSummary': () => ({ json: {} })
		});
		await page.goto('/fleet');
		const bar = page.locator('header > div');

		for (const [language, notifications] of [
			['English', 'Notifications'],
			['Deutsch', 'Benachrichtigungen']
		]) {
			if (language === 'Deutsch') {
				await page.getByRole('button', { name: 'Account' }).click();
				await page.getByRole('button', { name: 'Deutsch' }).click();
				await page.keyboard.press('Escape');
			}
			await expect(page.getByRole('link', { name: notifications })).toBeVisible();
			const height = (await bar.boundingBox())?.height ?? 0;
			expect(height, `${language}: header height`).toBeLessThan(56);
			await page.screenshot({
				path: `test-results/header-1280-${language}.png`,
				clip: { x: 0, y: 0, width: 1280, height: 120 }
			});
		}

		// The menu holds the account, theme, language and sign-out, and passes axe.
		await page.getByRole('button', { name: 'Konto' }).click();
		await expect(page.getByText('Angemeldet als Mia Muster')).toBeVisible();
		await expect(page.getByRole('button', { name: 'Abmelden' })).toBeVisible();
		const results = await new AxeBuilder({ page }).analyze();
		expect(results.violations).toEqual([]);
	});
});
