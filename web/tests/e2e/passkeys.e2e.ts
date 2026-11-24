import AxeBuilder from '@axe-core/playwright';
import { generateKeyPairSync, randomBytes } from 'node:crypto';
import type { Page } from '@playwright/test';
import { installed, mockApi, session } from './api';
import { expect, test } from './fixtures';

/**
 * Passkeys in the UI (M3-24) with Chromium's virtual authenticator against the mocked API: the
 * browser really creates and uses a credential; the server's checks are tested in Go. WebAuthn
 * needs a host name as RP ID, so these tests run on localhost.
 */
test.use({ baseURL: 'http://localhost:4173', expectedHttpErrors: [400, 401] });

const b64url = (b: Buffer) => b.toString('base64url');
const user = Buffer.from('0193f5a0000070008000000000000000', 'hex');

async function virtualAuthenticator(page: Page) {
	const cdp = await page.context().newCDPSession(page);
	await cdp.send('WebAuthn.enable');
	const { authenticatorId } = await cdp.send('WebAuthn.addVirtualAuthenticator', {
		options: {
			protocol: 'ctap2',
			transport: 'internal',
			hasResidentKey: true,
			hasUserVerification: true,
			isUserVerified: true,
			automaticPresenceSimulation: true
		}
	});
	return { cdp, authenticatorId };
}

const creation = () =>
	JSON.stringify({
		publicKey: {
			challenge: b64url(randomBytes(32)),
			rp: { id: 'localhost', name: 'SerMon' },
			user: { id: b64url(user), name: session.email, displayName: session.displayName },
			pubKeyCredParams: [{ type: 'public-key', alg: -7 }],
			authenticatorSelection: { residentKey: 'preferred', userVerification: 'preferred' },
			attestation: 'none'
		}
	});

const request = () =>
	JSON.stringify({
		publicKey: {
			challenge: b64url(randomBytes(32)),
			rpId: 'localhost',
			userVerification: 'required'
		}
	});

test('adds a passkey in the account security and passes axe', async ({ page }) => {
	await virtualAuthenticator(page);
	const finished: { credentialJson: string }[] = [];
	const keys: object[] = [];
	await mockApi(page, {
		...installed(true),
		'AuthService/GetSession': () => ({
			json: { session: { ...session, passkeysAvailable: true } }
		}),
		'TwoFactorService/GetTwoFactorStatus': () => ({ json: {} }),
		'PasskeyService/ListPasskeys': () => ({ json: { passkeys: keys } }),
		'PasskeyService/BeginPasskeyRegistration': () => ({
			json: { ceremonyId: 'c1', optionsJson: creation() }
		}),
		'PasskeyService/FinishPasskeyRegistration': (body) => {
			finished.push(body as { credentialJson: string });
			keys.push({ id: 'k1', name: 'Laptop', createTime: new Date().toISOString() });
			return { json: { passkey: keys[0] } };
		}
	});
	await page.goto('/settings/security');
	await expect(page.getByText('No passkey yet.')).toBeVisible();
	let results = await new AxeBuilder({ page }).analyze();
	expect(results.violations).toEqual([]);

	await page.getByLabel('Name of the passkey').fill('Laptop');
	await page.getByRole('button', { name: 'Add a passkey' }).click();
	await expect(page.getByText('Passkey “Laptop” added.')).toBeVisible();
	const answer = JSON.parse(finished[0].credentialJson);
	expect(answer.type).toBe('public-key');
	expect(answer.response.attestationObject.length).toBeGreaterThan(100);
	expect(
		JSON.parse(Buffer.from(answer.response.clientDataJSON, 'base64url').toString())
	).toMatchObject({
		type: 'webauthn.create',
		origin: 'http://localhost:4173'
	});
	await expect(page.getByRole('listitem').filter({ hasText: 'Laptop' })).toContainText(
		'never used'
	);
	results = await new AxeBuilder({ page }).analyze();
	expect(results.violations).toEqual([]);
});

test('signs in with a passkey of the authenticator', async ({ page }) => {
	const { cdp, authenticatorId } = await virtualAuthenticator(page);
	const { privateKey } = generateKeyPairSync('ec', { namedCurve: 'P-256' });
	await cdp.send('WebAuthn.addCredential', {
		authenticatorId,
		credential: {
			credentialId: randomBytes(16).toString('base64'),
			isResidentCredential: true,
			rpId: 'localhost',
			privateKey: privateKey.export({ format: 'der', type: 'pkcs8' }).toString('base64'),
			userHandle: user.toString('base64'),
			signCount: 1
		}
	});
	const finished: { purpose: string; credentialJson: string }[] = [];
	await mockApi(page, {
		...installed(false),
		'AuthService/GetLoginOptions': () => ({ json: { passkeys: true } }),
		'AuthService/BeginPasskey': () => ({ json: { ceremonyId: 'c1', optionsJson: request() } }),
		'AuthService/FinishPasskey': (body) => {
			finished.push(body as { purpose: string; credentialJson: string });
			return { json: { session } };
		}
	});
	// The explicit button; the autofill (conditional UI) would sign in by itself here.
	await page.addInitScript(() => {
		window.PublicKeyCredential.isConditionalMediationAvailable = async () => false;
	});
	await page.goto('/login');
	const results = await new AxeBuilder({ page }).analyze();
	expect(results.violations).toEqual([]);
	await page.getByRole('button', { name: 'Sign in with a passkey' }).click();
	await expect(page).toHaveURL('/fleet');
	const last = finished.at(-1)!;
	expect(last.purpose).toBe('PASSKEY_PURPOSE_LOGIN');
	expect(JSON.parse(last.credentialJson).response.userHandle).toBe(b64url(user));
});

test('signs in from the autofill where the browser offers it (conditional UI)', async ({
	page
}) => {
	const { cdp, authenticatorId } = await virtualAuthenticator(page);
	const { privateKey } = generateKeyPairSync('ec', { namedCurve: 'P-256' });
	await cdp.send('WebAuthn.addCredential', {
		authenticatorId,
		credential: {
			credentialId: randomBytes(16).toString('base64'),
			isResidentCredential: true,
			rpId: 'localhost',
			privateKey: privateKey.export({ format: 'der', type: 'pkcs8' }).toString('base64'),
			userHandle: user.toString('base64'),
			signCount: 1
		}
	});
	await mockApi(page, {
		...installed(false),
		'AuthService/GetLoginOptions': () => ({ json: { passkeys: true } }),
		'AuthService/BeginPasskey': () => ({ json: { ceremonyId: 'c1', optionsJson: request() } }),
		'AuthService/FinishPasskey': () => ({ json: { session } })
	});
	await page.goto('/login');
	const available = await page.evaluate(
		async () => (await window.PublicKeyCredential.isConditionalMediationAvailable?.()) ?? false
	);
	test.skip(!available, 'this Chromium offers no conditional UI');
	// The request starts with the focus of the e-mail field (M3-29); the virtual authenticator
	// answers it by itself, like picking the passkey.
	await page.getByLabel('Email address').focus();
	await expect(page).toHaveURL('/fleet');
});
