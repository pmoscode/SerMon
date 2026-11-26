import { expect, test } from '../e2e/fixtures';
import { admin, call, login, ownerSQL } from './stack';

/**
 * Passkeys against the real server (IMPLEMENTIERUNGSPLAN M3-24) with Chromium's virtual
 * authenticator; the relying party is localhost, the first allowed origin of the overlay. The
 * admin adds a passkey, signs in with it without a password, confirms the step-up of an update
 * by hand (M4-28) and of its deletion with it – the other demos sign in with the password and run
 * before.
 */

// The step-up asks once with FAILED_PRECONDITION (400), the browser notes it in the console.
test.use({ expectedHttpErrors: [400, 401] });

type Session = { session?: { email: string; passkeyCount: number; secondFactorEnabled: boolean } };

test('a passkey is added, signs in without a password and confirms the step-ups of an update by hand and its own deletion', async ({
	page,
	baseURL
}) => {
	test.setTimeout(15 * 60_000);
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
	// The autofill starts with the focus of the e-mail field (M3-29).
	await page.getByLabel('Email address').focus();
	await page.waitForURL('/fleet', { timeout: 3000 }).catch(async () => {
		await page.getByRole('button', { name: 'Sign in with a passkey' }).click();
		await page.waitForURL('/fleet');
	});
	session = await call<Session>(page.request, base, 'AuthService/GetSession', {});
	expect(session.session?.email).toBe(admin.email);

	// Updates by hand (M4-14, M4-28): db-01 kept its updates through the rollout (its veto is
	// lifted). "Update now" lists the packages in the dialog; the aged confirmation asks for the
	// passkey, the rollout of its group installs them, the audit log says who, when, what.
	type FleetUpdates = {
		nodes?: {
			nodeId: string;
			hostname: string;
			status?: string;
			permissions?: { remoteUpdates?: boolean };
		}[];
	};
	const db01 = async () =>
		(
			await call<FleetUpdates>(page.request, base, 'UpdateService/ListFleetUpdates', {})
		).nodes?.find((n) => n.hostname === 'db-01');
	await expect
		.poll(async () => (await db01())?.permissions?.remoteUpdates ?? false, {
			timeout: 240_000,
			intervals: [5_000]
		})
		.toBe(true);
	const manualStarted = Date.now();
	ownerSQL(
		`UPDATE sessions SET step_up_at = now() - interval '1 hour' WHERE user_id = (SELECT id FROM users WHERE email = '${admin.email}');`
	);
	await page.goto('/fleet');
	await page.getByRole('link', { name: 'db-01' }).click();
	await page.getByRole('tab', { name: 'Updates' }).click();
	await expect(
		page
			.getByRole('table', { name: 'Available updates' })
			.getByRole('cell', { name: /^sermon-e2e-demo/ })
	).toBeVisible({ timeout: 30_000 });
	await page.getByRole('button', { name: 'Update now' }).click();
	await page.getByRole('radio', { name: /All updates/ }).click();
	await expect(
		page
			.getByRole('table', { name: 'These packages will be updated' })
			.getByRole('cell', { name: 'sermon-e2e-extra' })
	).toBeVisible();
	await page.getByRole('button', { name: 'Update 2 packages' }).click();
	const stepUp = page.getByRole('dialog').filter({ hasText: /once more with your passkey/ });
	await expect(stepUp).toBeVisible();
	await stepUp.getByRole('button', { name: 'Confirm with a passkey' }).click();
	await expect(page.getByText(/Started – the rollout runs/)).toBeVisible({ timeout: 30_000 });
	await expect
		.poll(
			() =>
				ownerSQL(
					`SELECT r.state || ':' || string_agg(n.hostname || '=' || n.state, ',')
					 FROM update_rollouts r JOIN update_rollout_nodes n ON n.rollout_id = r.id
					 WHERE r.manual GROUP BY r.id, r.state`
				).trim(),
			{ timeout: 420_000, intervals: [10_000] }
		)
		.toBe('2:db-01=5');
	await expect
		.poll(async () => (await db01())?.status, { timeout: 240_000, intervals: [10_000] })
		.toBe('UPDATE_STATUS_CURRENT');
	console.log(`db-01 updated by hand after ${Math.round((Date.now() - manualStarted) / 1000)} s`);
	await page.goto('/admin');
	await page.getByRole('tab', { name: 'Audit log' }).click();
	const entry = page
		.getByRole('table', { name: 'Audit log' })
		.getByRole('row')
		.filter({ hasText: 'updates.start' });
	await expect(entry).toHaveCount(1);
	await expect(entry).toContainText(admin.email);
	// The audit names nodes by their ID: stable, while a host chooses its name.
	await expect(entry).toContainText(`nodes=${(await db01())!.nodeId}`);
	await expect(entry).toContainText('mode=all-auto');
	await page.screenshot({ path: 'test-results/demo-audit.png', fullPage: true });

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
