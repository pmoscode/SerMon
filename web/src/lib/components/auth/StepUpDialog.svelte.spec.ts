import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { api, Code } from '#lib/api/client.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { sessionState } from '#lib/stores/session.svelte.js';
import { browser, ssoState } from '#lib/stores/sso.svelte.js';
import { stepUpState } from '#lib/stores/stepup.svelte.js';
import { fakeAssertion, requestJson } from '#lib/test/webauthn.js';
import { fakeSession, PASSPHRASE, rpcError } from '#lib/test/fakes.js';
import StepUpDialog from './StepUpDialog.svelte';

describe('StepUpDialog', () => {
	beforeEach(() => {
		switchLocale('en');
		sessionState.status = 'authenticated';
		sessionState.current = fakeSession();
		vi.spyOn(api.auth, 'getLoginOptions').mockResolvedValue({ oidc: false, oidcName: '' } as never);
	});
	afterEach(() => {
		vi.restoreAllMocks();
		ssoState.reset();
		if (stepUpState.open) stepUpState.finish(false);
		sessionState.status = 'unknown';
		sessionState.current = undefined;
	});

	it('confirms the password and reports success', async () => {
		const confirm = vi
			.spyOn(api.auth, 'confirmPassword')
			.mockResolvedValue({ session: fakeSession() } as never);
		render(StepUpDialog);
		const confirmed = stepUpState.request();

		await page.getByLabelText('Password').fill(PASSPHRASE);
		await page.getByRole('button', { name: 'Confirm' }).click();

		await expect(confirmed).resolves.toBe(true);
		expect(confirm.mock.calls[0][0]).toEqual({ password: PASSPHRASE });
	});

	it('stays open with a message for a wrong password', async () => {
		vi.spyOn(api.auth, 'confirmPassword').mockRejectedValue(rpcError(Code.PermissionDenied));
		render(StepUpDialog);
		void stepUpState.request();

		await page.getByLabelText('Password').fill('wrong');
		await page.getByRole('button', { name: 'Confirm' }).click();

		await expect
			.element(page.getByRole('alert'))
			.toHaveTextContent('The password is wrong or the account is temporarily locked.');
		expect(stepUpState.open).toBe(true);
	});

	it('requires a password and can be cancelled', async () => {
		const confirm = vi.spyOn(api.auth, 'confirmPassword');
		render(StepUpDialog);
		const confirmed = stepUpState.request();

		await page.getByRole('button', { name: 'Confirm' }).click();
		await expect.element(page.getByLabelText('Password')).toHaveAttribute('aria-invalid', 'true');
		await page.getByRole('button', { name: 'Cancel' }).first().click();

		await expect(confirmed).resolves.toBe(false);
		expect(confirm).not.toHaveBeenCalled();
	});

	it('asks users with a second factor for a code instead of the password', async () => {
		const session = fakeSession();
		session.secondFactorEnabled = true;
		session.totpEnabled = true;
		sessionState.current = session;
		const confirm = vi
			.spyOn(api.auth, 'confirmSecondFactor')
			.mockResolvedValue({ session } as never);
		const password = vi.spyOn(api.auth, 'confirmPassword');
		render(StepUpDialog);
		const confirmed = stepUpState.request();

		await page.getByLabelText('Code').fill('123456');
		await page.getByRole('button', { name: 'Confirm' }).click();

		await expect(confirmed).resolves.toBe(true);
		expect(confirm.mock.calls[0][0]).toEqual({ code: '123456' });
		expect(password).not.toHaveBeenCalled();
	});

	// M3-22: users with an identity of the provider may confirm there; accounts from the provider
	// without a password or a second factor only there.
	it('offers the provider and leaves for it', async () => {
		vi.spyOn(api.auth, 'getLoginOptions').mockResolvedValue({
			oidc: true,
			oidcName: 'Keycloak'
		} as never);
		const begin = vi
			.spyOn(api.auth, 'beginOidc')
			.mockResolvedValue({ authorizationUrl: 'https://id.example/auth?x=1' } as never);
		const leave = vi.spyOn(browser, 'leave').mockImplementation(() => {});
		sessionState.current = { ...fakeSession(), oidcLinked: true };
		render(StepUpDialog);
		void stepUpState.request();

		await expect.element(page.getByLabelText('Password')).toBeVisible();
		await page.getByRole('button', { name: 'Confirm with Keycloak' }).click();
		await vi.waitFor(() => expect(leave).toHaveBeenCalledWith('https://id.example/auth?x=1'));
		expect(begin.mock.calls[0][0]).toMatchObject({ intent: 3 });
	});

	it('asks an account without password only for the provider', async () => {
		vi.spyOn(api.auth, 'getLoginOptions').mockResolvedValue({
			oidc: true,
			oidcName: 'Dex'
		} as never);
		sessionState.current = { ...fakeSession(), hasPassword: false, oidcLinked: true };
		render(StepUpDialog);
		void stepUpState.request();

		await expect.element(page.getByRole('button', { name: 'Confirm with Dex' })).toBeVisible();
		expect(page.getByLabelText('Password').elements()).toHaveLength(0);
		expect(page.getByRole('button', { name: 'Confirm', exact: true }).elements()).toHaveLength(0);
	});

	it('shows no provider to users without an identity', async () => {
		const options = vi
			.spyOn(api.auth, 'getLoginOptions')
			.mockResolvedValue({ oidc: true, oidcName: 'Dex' } as never);
		render(StepUpDialog);
		void stepUpState.request();
		await vi.waitFor(() => expect(options).toHaveBeenCalled());
		await expect.element(page.getByLabelText('Password')).toBeVisible();
		expect(page.getByRole('button', { name: /Dex/ }).elements()).toHaveLength(0);
	});

	// M3-24: a passkey confirms right in the dialog; with a second factor the password is no way.
	it('confirms with a passkey', async () => {
		vi.spyOn(api.auth, 'beginPasskey').mockResolvedValue({
			ceremonyId: 'c1',
			optionsJson: requestJson
		} as never);
		vi.spyOn(navigator.credentials, 'get').mockResolvedValue(fakeAssertion());
		const finish = vi
			.spyOn(api.auth, 'finishPasskey')
			.mockResolvedValue({ session: fakeSession() } as never);
		sessionState.current = {
			...fakeSession(),
			secondFactorEnabled: true,
			passkeyCount: 1,
			passkeysAvailable: true
		};
		render(StepUpDialog);
		const confirmed = stepUpState.request();
		await expect.element(page.getByText(/once more with your passkey/)).toBeVisible();
		expect(page.getByLabelText('Password').elements()).toHaveLength(0);
		await page.getByRole('button', { name: 'Confirm with a passkey' }).click();
		await expect(confirmed).resolves.toBe(true);
		expect(finish.mock.calls[0][0]).toMatchObject({ purpose: 3 });
	});

	it('keeps the dialog open when the passkey is refused', async () => {
		vi.spyOn(api.auth, 'beginPasskey').mockResolvedValue({
			ceremonyId: 'c1',
			optionsJson: requestJson
		} as never);
		vi.spyOn(navigator.credentials, 'get').mockResolvedValue(fakeAssertion());
		vi.spyOn(api.auth, 'finishPasskey').mockRejectedValue(rpcError(Code.PermissionDenied));
		sessionState.current = {
			...fakeSession(),
			secondFactorEnabled: true,
			totpEnabled: true,
			passkeyCount: 2,
			passkeysAvailable: true
		};
		render(StepUpDialog);
		void stepUpState.request();
		await expect.element(page.getByLabelText('Code')).toBeVisible();
		await page.getByRole('button', { name: 'Confirm with a passkey' }).click();
		await expect
			.element(page.getByRole('alert'))
			.toHaveTextContent('The passkey was not accepted.');
		expect(stepUpState.open).toBe(true);
	});

	it('offers no passkey without one', async () => {
		sessionState.current = { ...fakeSession(), passkeyCount: 0, passkeysAvailable: true };
		render(StepUpDialog);
		void stepUpState.request();
		await expect.element(page.getByLabelText('Password')).toBeVisible();
		expect(page.getByRole('button', { name: 'Confirm with a passkey' }).elements()).toHaveLength(0);
	});
});
