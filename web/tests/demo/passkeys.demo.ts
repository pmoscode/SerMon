import { expect, test } from '../e2e/fixtures';
import { admin, call, login, ownerSQL } from './stack';

/**
 * Passkeys against the real server (IMPLEMENTIERUNGSPLAN M3-24) with Chromium's virtual
 * authenticator; the relying party is localhost, the first allowed origin of the overlay. The
 * admin adds a passkey, signs in with it without a password, confirms a step-up with it and
 * deletes it again – the other demos sign in with the password and run before.
 */

// The step-up asks once with FAILED_PRECONDITION (400), the browser notes it in the console.
test.use({ expectedHttpErrors: [400, 401] });

type Session = { session?: { email: string; passkeyCount: number; secondFactorEnabled: boolean } };

test('a passkey is added, signs in without a password and confirms a step-up', async ({
	page,
	baseURL
}) => {
	const base = baseURL ?? '';
	const cdp = await page.context().newCDPSession(page);
	await cdp.send('WebAuthn.enable');
	await cdp.send('WebAuthn.addVirtualAuthenticator', {
		options: {
			protocol: 'ctap2',
			transport: 'internal',
			hasResidentKey: true,
			hasUserVerification: true,
			isUserVerified: true,
			automaticPresenceSimulation: true
		}
	});
	await login(page, admin.email);
	await page.goto('/settings/security');
	await page.getByLabel('Name of the passkey').fill('Demo laptop');
	await page.getByRole('button', { name: 'Add a passkey' }).click();
	await expect(page.getByText('Passkey “Demo laptop” added.')).toBeVisible();
	let session = await call<Session>(page.request, base, 'AuthService/GetSession', {});
	expect(session.session?.passkeyCount).toBe(1);
	expect(session.session?.secondFactorEnabled).toBe(true);

	// Signed out, back in with the passkey only (the autofill or the button, whichever comes first).
	await call(page.request, base, 'AuthService/Logout', {});
	await page.goto('/login');
	await page.waitForURL('/fleet', { timeout: 3000 }).catch(async () => {
		await page.getByRole('button', { name: 'Sign in with a passkey' }).click();
		await page.waitForURL('/fleet');
	});
	session = await call<Session>(page.request, base, 'AuthService/GetSession', {});
	expect(session.session?.email).toBe(admin.email);

	// The step-up of a sensitive action: deleting the passkey after the confirmation aged.
	ownerSQL(
		`UPDATE sessions SET step_up_at = now() - interval '1 hour' WHERE user_id = (SELECT id FROM users WHERE email = '${admin.email}');`
	);
	await page.goto('/settings/security');
	await page.getByRole('button', { name: 'Delete passkey Demo laptop' }).click();
	await page.getByRole('button', { name: 'Really delete' }).click();
	const dialog = page.getByRole('dialog');
	await expect(dialog.getByText(/once more with your passkey/)).toBeVisible();
	await dialog.getByRole('button', { name: 'Confirm with a passkey' }).click();
	await expect(page.getByText('No passkey yet.')).toBeVisible();
	session = await call<Session>(page.request, base, 'AuthService/GetSession', {});
	expect(session.session?.passkeyCount ?? 0).toBe(0);
});
