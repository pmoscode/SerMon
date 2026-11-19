import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { api, Code } from '#lib/api/client.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { sessionState } from '#lib/stores/session.svelte.js';
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
});
