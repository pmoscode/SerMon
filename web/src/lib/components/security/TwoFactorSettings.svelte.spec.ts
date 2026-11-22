import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { api, Code } from '#lib/api/client.js';
import { adminApi } from '#lib/api/admin.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { m } from '#lib/paraglide/messages.js';
import { sessionState } from '#lib/stores/session.svelte.js';
import { stepUpState } from '#lib/stores/stepup.svelte.js';
import { fakeSession, rpcError, stepUpError } from '#lib/test/fakes.js';
import TwoFactorSettings from './TwoFactorSettings.svelte';

// The RFC 6238 test secret in base32 – a fixture, not a secret.
const rfcSecret = 'GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ'; // betterleaks:allow

const codes = Array.from(
	{ length: 10 },
	(_, i) => `aaaa-bbbb-cccc-dd${String(i).padStart(2, 'a')}`
);

describe('TwoFactorSettings', () => {
	beforeEach(() => {
		switchLocale('en');
		sessionState.status = 'authenticated';
		sessionState.current = fakeSession();
		vi.spyOn(api.auth, 'getSession').mockResolvedValue({ session: fakeSession() } as never);
	});
	afterEach(() => {
		vi.restoreAllMocks();
		if (stepUpState.open) stepUpState.finish(false);
		sessionState.status = 'unknown';
		sessionState.current = undefined;
	});

	it('sets up TOTP with a QR code and shows the recovery codes once', async () => {
		const status = vi
			.spyOn(adminApi.twoFactor, 'getTwoFactorStatus')
			.mockResolvedValueOnce({
				totpEnabled: false,
				recoveryCodesLeft: 0,
				requiredByOrganization: true
			} as never)
			.mockResolvedValueOnce({ totpEnabled: true, recoveryCodesLeft: 10 } as never);
		vi.spyOn(adminApi.twoFactor, 'beginTotpSetup').mockResolvedValue({
			secret: rfcSecret,
			uri: `otpauth://totp/SerMon:mia@example.org?secret=${rfcSecret}&issuer=SerMon`
		} as never);
		const confirm = vi
			.spyOn(adminApi.twoFactor, 'confirmTotpSetup')
			.mockRejectedValueOnce(rpcError(Code.PermissionDenied))
			.mockResolvedValueOnce({ recoveryCodes: codes } as never);
		render(TwoFactorSettings);

		await expect
			.element(
				page.getByText(
					'This organization requires two-factor authentication. Please set it up now.'
				)
			)
			.toBeVisible();
		await page.getByRole('button', { name: 'Set up' }).click();
		await expect
			.element(page.getByRole('img', { name: 'QR code for the authenticator app' }))
			.toBeVisible();
		await expect.element(page.getByText(rfcSecret)).toBeInTheDocument();

		await page.getByLabelText('Enter the 6-digit code shown').fill('000000');
		await page.getByRole('button', { name: 'Activate' }).click();
		await expect.element(page.getByRole('alert')).toHaveTextContent(m.security_setup_wrong_code());
		await page.getByLabelText('Enter the 6-digit code shown').fill('123456');
		await page.getByRole('button', { name: 'Activate' }).click();

		await expect.element(page.getByText(codes[9])).toBeVisible();
		expect(confirm.mock.calls[1][0]).toEqual({ code: '123456' });
		await page.getByRole('button', { name: 'Saved, done' }).click();
		await expect.element(page.getByText('Set up. 10 recovery codes left.')).toBeVisible();
		expect(status).toHaveBeenCalledTimes(2);
	});

	it('asks for the step-up before the setup', async () => {
		vi.spyOn(adminApi.twoFactor, 'getTwoFactorStatus').mockResolvedValue({
			totpEnabled: false
		} as never);
		const begin = vi
			.spyOn(adminApi.twoFactor, 'beginTotpSetup')
			.mockRejectedValueOnce(stepUpError())
			.mockResolvedValueOnce({ secret: 'ABC', uri: 'otpauth://totp/x?secret=ABC' } as never);
		render(TwoFactorSettings);

		await page.getByRole('button', { name: 'Set up' }).click();
		await vi.waitFor(() => expect(stepUpState.open).toBe(true));
		stepUpState.finish(true);

		await expect
			.element(page.getByRole('img', { name: 'QR code for the authenticator app' }))
			.toBeVisible();
		expect(begin).toHaveBeenCalledTimes(2);
	});

	it('replaces the recovery codes and turns off after a second click', async () => {
		vi.spyOn(adminApi.twoFactor, 'getTwoFactorStatus').mockResolvedValue({
			totpEnabled: true,
			recoveryCodesLeft: 3
		} as never);
		vi.spyOn(adminApi.twoFactor, 'regenerateRecoveryCodes').mockResolvedValue({
			recoveryCodes: codes
		} as never);
		const disable = vi.spyOn(adminApi.twoFactor, 'disableTwoFactor').mockResolvedValue({} as never);
		render(TwoFactorSettings);

		await expect.element(page.getByText('Set up. 3 recovery codes left.')).toBeVisible();
		await page.getByRole('button', { name: 'New recovery codes' }).click();
		await expect.element(page.getByText(codes[0])).toBeVisible();
		await page.getByRole('button', { name: 'Saved, done' }).click();

		await page.getByRole('button', { name: 'Turn off' }).click();
		expect(disable).not.toHaveBeenCalled();
		await page.getByRole('button', { name: 'Really turn off' }).click();
		await vi.waitFor(() => expect(disable).toHaveBeenCalledOnce());
	});
});
