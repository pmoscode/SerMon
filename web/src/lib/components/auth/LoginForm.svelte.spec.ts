import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { api, Code } from '#lib/api/client.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { m } from '#lib/paraglide/messages.js';
import { sessionState } from '#lib/stores/session.svelte.js';
import { browser, ssoState } from '#lib/stores/sso.svelte.js';
import { fakeAssertion, notAllowed, requestJson } from '#lib/test/webauthn.js';
import { fakeSession, PASSPHRASE, rpcError } from '#lib/test/fakes.js';
import LoginForm from './LoginForm.svelte';

async function fillAndSubmit(email: string, password: string) {
	await page.getByLabelText('Email address').fill(email);
	await page.getByLabelText('Password').fill(password);
	await page.getByRole('button', { name: 'Sign in' }).click();
}

describe('LoginForm', () => {
	beforeEach(() => switchLocale('en'));
	afterEach(() => {
		vi.restoreAllMocks();
		ssoState.reset();
		sessionState.status = 'unknown';
		sessionState.current = undefined;
	});

	it('requires both fields before it calls the server', async () => {
		const login = vi.spyOn(api.auth, 'login');
		render(LoginForm, { onLoggedIn: vi.fn() });

		await page.getByRole('button', { name: 'Sign in' }).click();

		await expect
			.element(page.getByLabelText('Email address'))
			.toHaveAttribute('aria-invalid', 'true');
		await expect.element(page.getByLabelText('Password')).toHaveAttribute('aria-invalid', 'true');
		expect(login).not.toHaveBeenCalled();
	});

	it('logs in and reports success', async () => {
		const login = vi
			.spyOn(api.auth, 'login')
			.mockResolvedValue({ session: fakeSession() } as never);
		const onLoggedIn = vi.fn();
		render(LoginForm, { onLoggedIn });

		await fillAndSubmit('  mia@example.org ', PASSPHRASE);

		await vi.waitFor(() => expect(onLoggedIn).toHaveBeenCalledOnce());
		expect(login.mock.calls[0][0]).toEqual({
			email: 'mia@example.org',
			password: PASSPHRASE
		});
		expect(sessionState.status).toBe('authenticated');
		expect(sessionState.organization?.name).toBe('Alpha');
	});

	it.each([
		[Code.Unauthenticated, 'Email address or password is wrong.'],
		[Code.ResourceExhausted, 'Too many attempts. Please try again in a minute.'],
		[Code.Unavailable, "The server can't be reached right now. Please try again in a moment."]
	])('shows the failure for %s without telling which field was wrong', async (code, text) => {
		vi.spyOn(api.auth, 'login').mockRejectedValue(rpcError(code));
		const onLoggedIn = vi.fn();
		render(LoginForm, { onLoggedIn });

		await fillAndSubmit('mia@example.org', 'wrong');

		await expect.element(page.getByRole('alert')).toHaveTextContent(text);
		expect(onLoggedIn).not.toHaveBeenCalled();
		await expect.element(page.getByRole('button', { name: 'Sign in' })).toBeEnabled();
	});

	it('asks for the second factor and logs in with the code', async () => {
		vi.spyOn(api.auth, 'login').mockResolvedValue({ secondFactorRequired: true } as never);
		const verify = vi
			.spyOn(api.auth, 'verifySecondFactor')
			.mockRejectedValueOnce(rpcError(Code.PermissionDenied))
			.mockResolvedValueOnce({ session: fakeSession() } as never);
		const onLoggedIn = vi.fn();
		render(LoginForm, { onLoggedIn });

		await fillAndSubmit('mia@example.org', PASSPHRASE);
		await expect.element(page.getByLabelText('Code')).toBeVisible();
		expect(onLoggedIn).not.toHaveBeenCalled();

		await page.getByLabelText('Code').fill('000000');
		await page.getByRole('button', { name: 'Confirm' }).click();
		await expect
			.element(page.getByRole('alert'))
			.toHaveTextContent(
				'The code is wrong or was already used – or the account is temporarily locked.'
			);

		await page.getByLabelText('Code').fill(' 123 456 ');
		await page.getByRole('button', { name: 'Confirm' }).click();
		await vi.waitFor(() => expect(onLoggedIn).toHaveBeenCalledOnce());
		expect(verify.mock.calls[1][0]).toEqual({ code: '123 456' });
		expect(sessionState.status).toBe('authenticated');
	});

	it('starts again with the password when the waiting login expired', async () => {
		vi.spyOn(api.auth, 'login').mockResolvedValue({ secondFactorRequired: true } as never);
		vi.spyOn(api.auth, 'verifySecondFactor').mockRejectedValue(rpcError(Code.Unauthenticated));
		render(LoginForm, { onLoggedIn: vi.fn() });

		await fillAndSubmit('mia@example.org', PASSPHRASE);
		await page.getByLabelText('Code').fill('123456');
		await page.getByRole('button', { name: 'Confirm' }).click();

		await expect.element(page.getByRole('alert')).toHaveTextContent(m.login_error_code_expired());
		await expect.element(page.getByLabelText('Password')).toBeVisible();
	});

	// M3-22: the provider's button only with a provider; it starts the sign-in with the target.
	it('signs in over the provider when the instance has one', async () => {
		render(LoginForm, { onLoggedIn: vi.fn(), next: '/alerts?tab=rules' });
		await expect.element(page.getByRole('button', { name: 'Sign in' })).toBeVisible();
		expect(page.getByRole('button', { name: /Sign in with/ }).elements()).toHaveLength(0);

		ssoState.enabled = true;
		ssoState.name = 'Keycloak';
		const begin = vi
			.spyOn(api.auth, 'beginOidc')
			.mockResolvedValueOnce({ authorizationUrl: 'https://id.example/auth' } as never)
			.mockRejectedValueOnce(rpcError(Code.Unavailable));
		const leave = vi.spyOn(browser, 'leave').mockImplementation(() => {});
		await page.getByRole('button', { name: 'Sign in with Keycloak' }).click();
		await vi.waitFor(() => expect(leave).toHaveBeenCalledWith('https://id.example/auth'));
		expect(begin.mock.calls[0][0]).toEqual({ intent: 1, next: '/alerts?tab=rules' });
	});

	it('returns only to paths of the app', async () => {
		ssoState.enabled = true;
		ssoState.name = 'Keycloak';
		const begin = vi
			.spyOn(api.auth, 'beginOidc')
			.mockResolvedValue({ authorizationUrl: 'https://id.example/auth' } as never);
		vi.spyOn(browser, 'leave').mockImplementation(() => {});
		render(LoginForm, { onLoggedIn: vi.fn(), next: '//evil.example/steal' });
		await page.getByRole('button', { name: 'Sign in with Keycloak' }).click();
		await vi.waitFor(() => expect(begin).toHaveBeenCalledWith({ intent: 1, next: '/fleet' }));
	});

	it('says so when the provider is down', async () => {
		ssoState.enabled = true;
		ssoState.name = 'Keycloak';
		vi.spyOn(api.auth, 'beginOidc').mockRejectedValue(rpcError(Code.Unavailable));
		render(LoginForm, { onLoggedIn: vi.fn() });
		await page.getByRole('button', { name: 'Sign in with Keycloak' }).click();
		await expect.element(page.getByText(/identity provider can't be reached/)).toBeVisible();
	});

	it('starts with the code after the provider for users with a second factor', async () => {
		const verify = vi
			.spyOn(api.auth, 'verifySecondFactor')
			.mockResolvedValue({ session: fakeSession() } as never);
		const onLoggedIn = vi.fn();
		render(LoginForm, { onLoggedIn, initialStep: 'code' });
		await page.getByLabelText('Code').fill('123456');
		await page.getByRole('button', { name: /Confirm|Verify|Continue/ }).click();
		await vi.waitFor(() => expect(onLoggedIn).toHaveBeenCalledOnce());
		expect(verify.mock.calls[0][0]).toEqual({ code: '123456' });
	});

	// M3-24: passkeys instead of the password and instead of the code.
	it('signs in with a passkey and offers it for the second factor', async () => {
		ssoState.passkeys = true;
		vi.spyOn(window.PublicKeyCredential, 'isConditionalMediationAvailable').mockResolvedValue(
			false
		);
		const begin = vi
			.spyOn(api.auth, 'beginPasskey')
			.mockResolvedValue({ ceremonyId: 'c1', optionsJson: requestJson } as never);
		vi.spyOn(navigator.credentials, 'get').mockResolvedValue(fakeAssertion());
		const finish = vi
			.spyOn(api.auth, 'finishPasskey')
			.mockResolvedValue({ session: fakeSession() } as never);
		const onLoggedIn = vi.fn();
		render(LoginForm, { onLoggedIn });
		await expect
			.element(page.getByLabelText('Email address'))
			.toHaveAttribute('autocomplete', 'username webauthn');
		await page.getByRole('button', { name: 'Sign in with a passkey' }).click();
		await vi.waitFor(() => expect(onLoggedIn).toHaveBeenCalledOnce());
		expect(begin).toHaveBeenCalledWith({ purpose: 1 });
		expect(finish.mock.calls[0][0]).toMatchObject({ purpose: 1, ceremonyId: 'c1' });
		expect(JSON.parse(finish.mock.calls[0][0].credentialJson as string).response.userHandle).toBe(
			'Bw'
		);
		expect(sessionState.status).toBe('authenticated');
	});

	it('uses the passkey instead of the code and explains refusals and cancels', async () => {
		ssoState.passkeys = true;
		vi.spyOn(window.PublicKeyCredential, 'isConditionalMediationAvailable').mockResolvedValue(
			false
		);
		const begin = vi.spyOn(api.auth, 'beginPasskey').mockResolvedValue({
			ceremonyId: 'c1',
			optionsJson: requestJson
		} as never);
		vi.spyOn(navigator.credentials, 'get')
			.mockRejectedValueOnce(notAllowed())
			.mockResolvedValue(fakeAssertion());
		vi.spyOn(api.auth, 'finishPasskey')
			.mockRejectedValueOnce(rpcError(Code.PermissionDenied))
			.mockResolvedValue({ session: fakeSession() } as never);
		const onLoggedIn = vi.fn();
		render(LoginForm, { onLoggedIn, initialStep: 'code' });
		const button = page.getByRole('button', { name: 'Use a passkey instead of the code' });
		await button.click();
		await expect.element(button).toBeEnabled();
		expect(page.getByRole('alert').elements()).toHaveLength(0);
		await button.click();
		await expect
			.element(page.getByRole('alert'))
			.toHaveTextContent('The passkey was not accepted.');
		await button.click();
		await vi.waitFor(() => expect(onLoggedIn).toHaveBeenCalledOnce());
		expect(begin).toHaveBeenLastCalledWith({ purpose: 2 });
	});

	it('waits for the passkey in the autofill where the browser offers it', async () => {
		ssoState.passkeys = true;
		vi.spyOn(window.PublicKeyCredential, 'isConditionalMediationAvailable').mockResolvedValue(true);
		vi.spyOn(api.auth, 'beginPasskey').mockResolvedValue({
			ceremonyId: 'c1',
			optionsJson: requestJson
		} as never);
		const get = vi.spyOn(navigator.credentials, 'get').mockResolvedValue(fakeAssertion());
		vi.spyOn(api.auth, 'finishPasskey').mockResolvedValue({ session: fakeSession() } as never);
		const onLoggedIn = vi.fn();
		render(LoginForm, { onLoggedIn });
		await vi.waitFor(() => expect(onLoggedIn).toHaveBeenCalledOnce());
		expect(get.mock.calls[0][0]).toMatchObject({ mediation: 'conditional' });
	});

	it('shows no passkey button without passkeys on the server', async () => {
		render(LoginForm, { onLoggedIn: vi.fn() });
		await expect
			.element(page.getByLabelText('Email address'))
			.toHaveAttribute('autocomplete', 'username');
		expect(page.getByRole('button', { name: /passkey/ }).elements()).toHaveLength(0);
	});
});
