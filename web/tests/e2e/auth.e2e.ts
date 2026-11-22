import AxeBuilder from '@axe-core/playwright';
import { installed, mockApi, session, unauthenticated } from './api';
import { expect, test } from './fixtures';

test.describe('login and app shell (mocked API)', () => {
	test('logs in, returns to the requested page and shows the shell', async ({ page }) => {
		await mockApi(page, {
			...installed(false),
			'AuthService/Login': (body) =>
				(body as { password: string }).password === 'correct horse battery'
					? { json: { session } }
					: unauthenticated
		});
		await page.goto('/fleet?view=list');
		await expect(page).toHaveURL('/login?redirect=%2Ffleet%3Fview%3Dlist');

		await page.getByLabel('Email address').fill('mia@example.org');
		await page.getByLabel('Password').fill('wrong password');
		await page.getByRole('button', { name: 'Sign in' }).click();
		await expect(page.getByRole('alert')).toHaveText('Email address or password is wrong.');

		await page.getByLabel('Password').fill('correct horse battery');
		await page.getByRole('button', { name: 'Sign in' }).click();

		await expect(page).toHaveURL('/fleet?view=list');
		await expect(page.getByRole('heading', { level: 1 })).toHaveText('Fleet');
		await expect(page.getByRole('link', { name: 'Fleet' })).toHaveAttribute('aria-current', 'page');
		await expect(page.getByRole('combobox', { name: 'Organization' })).toHaveValue(
			session.activeOrganizationId
		);
		await expect(page.getByText('No nodes yet')).toBeVisible();
	});

	for (const theme of ['Light', 'Dark']) {
		test(`the shell has no accessibility violations (${theme})`, async ({ page }) => {
			await mockApi(page, installed(true));
			await page.goto('/fleet');
			// Theme and language live in the account menu (M2-23).
			await page.getByRole('button', { name: 'Account' }).click();
			await page.getByRole('button', { name: theme, exact: true }).click();
			await page.keyboard.press('Escape');
			await expect(page.getByRole('dialog')).toHaveCount(0);
			await expect(page.getByText('No nodes yet')).toBeVisible();
			// Colors fade on a theme switch: measure the contrast once they settled.
			await page.evaluate(() => Promise.all(document.getAnimations().map((a) => a.finished)));

			const results = await new AxeBuilder({ page }).analyze();

			expect(results.violations).toEqual([]);
		});
	}

	test('keeps the dark theme across reloads from the first paint', async ({ page }) => {
		await mockApi(page, installed(false));
		await page.goto('/login');
		await page.getByRole('button', { name: 'Dark', exact: true }).click();

		await page.reload({ waitUntil: 'commit' });
		await page.waitForFunction(() => document.documentElement !== null);

		expect(await page.evaluate(() => document.documentElement.classList.contains('dark'))).toBe(
			true
		);
		await expect(page.getByRole('button', { name: 'Dark', exact: true })).toHaveAttribute(
			'aria-pressed',
			'true'
		);
	});

	test('switches the organization', async ({ page }) => {
		const beta = { ...session, activeOrganizationId: session.organizations[1].id };
		const calls = await mockApi(page, {
			...installed(true),
			'AuthService/SwitchOrganization': () => ({ json: { session: beta } })
		});
		await page.goto('/fleet');

		await page.getByRole('combobox', { name: 'Organization' }).selectOption('Beta AG');

		await expect(page.getByRole('combobox', { name: 'Organization' })).toHaveValue(
			beta.activeOrganizationId
		);
		expect(calls).toContain('AuthService/SwitchOrganization');
	});

	test('logs out', async ({ page }) => {
		await mockApi(page, { ...installed(true), 'AuthService/Logout': () => ({ json: {} }) });
		await page.goto('/fleet');

		await page.getByRole('button', { name: 'Account' }).click();
		await page.getByRole('button', { name: 'Sign out' }).click();

		await expect(page).toHaveURL('/login?reason=logged_out');
		await expect(page.getByRole('status')).toHaveText('Signed out.');
	});

	test('never redirects to another host after login', async ({ page }) => {
		await mockApi(page, {
			...installed(false),
			'AuthService/Login': () => ({ json: { session } })
		});
		await page.goto('/login?redirect=//evil.example/steal');

		await page.getByLabel('Email address').fill('mia@example.org');
		await page.getByLabel('Password').fill('correct horse battery');
		await page.getByRole('button', { name: 'Sign in' }).click();

		await expect(page).toHaveURL('/fleet');
	});
});

test.describe('first start (mocked API)', () => {
	test('leads through the setup to the login', async ({ page }) => {
		let required = true;
		await mockApi(page, {
			'SetupService/GetSetupStatus': () => ({ json: { setupRequired: required } }),
			'SetupService/CompleteSetup': () => {
				required = false;
				return { json: {} };
			}
		});
		await page.goto('/');
		await expect(page).toHaveURL('/setup');
		await expect(page.getByRole('heading', { level: 1 })).toHaveText('Set up SerMon');
		const results = await new AxeBuilder({ page }).analyze();
		expect(results.violations).toEqual([]);

		await page.getByLabel('Setup token').fill('tok-123');
		await page.getByLabel('Organization name').fill('Acme');
		await page.getByLabel('Name', { exact: true }).fill('Mia');
		await page.getByLabel('Email address').fill('mia@example.org');
		await page.getByLabel('Password', { exact: true }).fill('a long passphrase');
		await page.getByLabel('Repeat password').fill('a long passphrase');
		await page.getByRole('button', { name: 'Set up' }).click();

		await expect(page).toHaveURL('/login?reason=setup_done');
		await expect(page.getByRole('status')).toHaveText(
			'Setup is complete. Sign in with the new account now.'
		);
	});
});
