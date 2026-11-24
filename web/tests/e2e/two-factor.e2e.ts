import AxeBuilder from '@axe-core/playwright';
import { installed, mockApi, session } from './api';
import { expect, test } from './fixtures';

// The RFC 6238 test secret in base32 – a fixture, not a secret.
const secret = 'GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ'; // betterleaks:allow
const uri = `otpauth://totp/SerMon:mia@example.org?algorithm=SHA1&digits=6&issuer=SerMon&period=30&secret=${secret}`;
const codes = Array.from(
	{ length: 10 },
	(_, i) => `abcd-efgh-ijkl-mn${'pqrstuvwxy'[i]}${'pqrstuvwxy'[i]}`
);

test.describe('two-factor authentication (mocked API)', () => {
	test('asks for the code after the password', async ({ page }) => {
		await mockApi(page, {
			...installed(false),
			'AuthService/Login': () => ({ json: { secondFactorRequired: true } }),
			'AuthService/VerifySecondFactor': (body) =>
				(body as { code: string }).code === '123456'
					? { json: { session } }
					: { status: 403, json: { code: 'permission_denied', message: 'wrong code' } }
		});
		await page.goto('/login');
		await page.getByLabel('Email address').fill('mia@example.org');
		await page.getByLabel('Password').fill('correct horse battery');
		await page.getByRole('button', { name: 'Sign in', exact: true }).click();

		await expect(page.getByLabel('Code')).toBeFocused();
		const results = await new AxeBuilder({ page }).analyze();
		expect(results.violations).toEqual([]);
		await page.getByLabel('Code').fill('123456');
		await page.getByRole('button', { name: 'Confirm' }).click();

		await expect(page).toHaveURL('/fleet');
	});

	test('sets up TOTP with a QR code and recovery codes', async ({ page }) => {
		let enabled = false;
		await mockApi(page, {
			...installed(true),
			'TwoFactorService/GetTwoFactorStatus': () => ({
				json: { totpEnabled: enabled, recoveryCodesLeft: enabled ? 10 : 0 }
			}),
			'TwoFactorService/BeginTotpSetup': () => ({ json: { secret, uri } }),
			'TwoFactorService/ConfirmTotpSetup': () => {
				enabled = true;
				return { json: { recoveryCodes: codes } };
			}
		});
		await page.goto('/settings/security');
		await page.getByRole('button', { name: 'Set up' }).click();

		const qr = page.getByRole('img', { name: 'QR code for the authenticator app' });
		await expect(qr).toBeVisible();
		await qr.screenshot({ path: 'test-results/totp-qr.png' });
		const results = await new AxeBuilder({ page }).analyze();
		expect(results.violations).toEqual([]);

		await page.getByLabel('Enter the 6-digit code shown').fill('123456');
		await page.getByRole('button', { name: 'Activate' }).click();
		await expect(page.getByText(codes[0])).toBeVisible();
		await page.getByRole('button', { name: 'Saved, done' }).click();
		await expect(page.getByText('Set up. 10 recovery codes left.')).toBeVisible();
	});

	test('leads to the setup while the organization requires it', async ({ page }) => {
		await mockApi(page, {
			'SetupService/GetSetupStatus': () => ({ json: { setupRequired: false } }),
			'AuthService/GetSession': () => ({
				json: { session: { ...session, secondFactorSetupRequired: true } }
			}),
			'TwoFactorService/GetTwoFactorStatus': () => ({ json: { requiredByOrganization: true } }),
			'AuthService/GetLoginOptions': () => ({ json: {} }),
			'AuthService/ListOidcIdentities': () => ({ json: {} }),
			'PasskeyService/ListPasskeys': () => ({ json: {} })
		});
		await page.goto('/fleet');

		await expect(page).toHaveURL('/settings/security');
		await expect(page.getByRole('alert').first()).toHaveText(
			'This organization requires two-factor authentication. The other pages are available once it is set up.'
		);
	});
});

// M3-22: the identities of the provider in the account security, unlinking with the step-up.
test('lists the linked identity, says when one is taken and unlinks it', async ({ page }) => {
	let linked = true;
	await mockApi(page, {
		...installed(true),
		'AuthService/GetSession': () => ({ json: { session: { ...session, oidcLinked: linked } } }),
		'AuthService/GetLoginOptions': () => ({ json: { oidc: true, oidcName: 'Keycloak' } }),
		'TwoFactorService/GetTwoFactorStatus': () => ({ json: {} }),
		'AuthService/ListOidcIdentities': () => ({
			json: linked
				? {
						identities: [
							{
								issuer: 'https://id.example/realms/ops',
								subject: 's1',
								email: 'mia@id.example',
								createTime: new Date().toISOString()
							}
						]
					}
				: {}
		}),
		'AuthService/UnlinkOidcIdentity': () => {
			linked = false;
			return { json: {} };
		}
	});
	await page.goto('/settings/security?sso=taken');
	await expect(page.getByText('mia@id.example')).toBeVisible();
	await expect(page.getByText(/linked to another account already/)).toBeVisible();
	const results = await new AxeBuilder({ page }).analyze();
	expect(results.violations).toEqual([]);
	await page.getByRole('button', { name: 'Unlink' }).click();
	await page.getByRole('button', { name: 'Really unlink' }).click();
	await expect(page.getByRole('button', { name: 'Link with Keycloak' })).toBeVisible();
});
