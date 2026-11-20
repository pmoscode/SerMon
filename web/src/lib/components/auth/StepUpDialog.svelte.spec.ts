import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { api, Code } from '#lib/api/client.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { sessionState } from '#lib/stores/session.svelte.js';
import { stepUpState } from '#lib/stores/stepup.svelte.js';
import { fakeSession, PASSPHRASE, rpcError } from '#lib/test/fakes.js';
import StepUpDialog from './StepUpDialog.svelte';

describe('StepUpDialog', () => {
	beforeEach(() => {
		switchLocale('en');
		sessionState.status = 'authenticated';
		sessionState.current = fakeSession();
	});
	afterEach(() => {
		vi.restoreAllMocks();
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
});
