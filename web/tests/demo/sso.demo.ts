import { expect, test } from '../e2e/fixtures';
import { admin, call, login } from './stack';

/**
 * Sign-in over a real OpenID Connect provider (IMPLEMENTIERUNGSPLAN M3-22): Dex of the overlay,
 * whose mock connector signs in "Kilgore Trout" in the group "authors" without a login page.
 * The admin of the installation maps the group to operator in the UI; Kilgore signs in over Dex,
 * gets an account (just-in-time) with the role, and loses it at the next sign-in once the
 * mapping is gone.
 */

const kilgore = 'kilgore@kilgore.trout';

type Session = { session?: { email: string; enrollGroupIds: string[]; hasPassword: boolean } };

test('a sign-in over Dex creates the account with the mapped role and the next one takes it away', async ({
	page,
	browser,
	baseURL
}) => {
	const base = baseURL ?? '';
	await login(page, admin.email);
	await page.goto('/admin');
	await page.getByRole('tab', { name: 'SSO groups' }).click();
	await expect(page.getByText(/get an account and become members/)).toBeVisible();
	await page.getByLabel('Group at the provider').fill('authors');
	await page
		.getByRole('tabpanel')
		.getByLabel('Role', { exact: true })
		.selectOption({ label: 'Operator' });
	await page.getByRole('button', { name: 'Create' }).click();
	await expect(page.getByRole('row').filter({ hasText: 'authors' })).toContainText('Operator');

	// Kilgore in a browser of their own: the button of the login page, Dex, back into the app.
	// This page goes to Dex, so it runs without the guard against foreign hosts.
	const context = await browser.newContext({ ignoreHTTPSErrors: true, baseURL: base });
	const sso = await context.newPage();
	async function signInOverDex() {
		await sso.goto('/login');
		await sso.getByRole('button', { name: 'Sign in with Dex' }).click();
		await expect(sso).toHaveURL(/\/fleet$/);
		return call<Session>(sso.request, base, 'AuthService/GetSession', {});
	}
	let session = await signInOverDex();
	expect(session.session?.email).toBe(kilgore);
	expect(session.session?.hasPassword).toBeFalsy();
	expect(session.session?.enrollGroupIds).toEqual(['*']);

	// The member list shows the role from SSO.
	await page.reload();
	await expect(page.getByRole('listitem').filter({ hasText: kilgore })).toContainText('SSO');

	// The admin deletes the mapping; Kilgore's next sign-in has no role anymore.
	await page.getByRole('tab', { name: 'SSO groups' }).click();
	await page.getByRole('button', { name: 'Delete the mapping of authors' }).click();
	await page.getByRole('button', { name: 'Really delete' }).click();
	await expect(page.getByText('No mappings.')).toBeVisible();

	await call(sso.request, base, 'AuthService/Logout', {});
	session = await signInOverDex();
	expect(session.session?.email).toBe(kilgore);
	expect(session.session?.enrollGroupIds ?? []).toEqual([]);
	await context.close();
});
