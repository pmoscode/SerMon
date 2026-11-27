import { create } from '@bufbuild/protobuf';
import { timestampFromDate } from '@bufbuild/protobuf/wkt';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { Code } from '#lib/api/client.js';
import { accountApi } from '#lib/api/account.js';
import { GetAccountResponseSchema } from '#lib/api/gen/sermon/web/v1/account_pb.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { sessionState } from '#lib/stores/session.svelte.js';
import { stepUpState } from '#lib/stores/stepup.svelte.js';
import { rpcError, stepUpError } from '#lib/test/fakes.js';
import AccountSettings from './AccountSettings.svelte';

const hostile = '<img src=x onerror=alert(1)>';

function account(pending = '') {
	return create(GetAccountResponseSchema, {
		email: 'alice@example.org',
		displayName: hostile,
		createTime: timestampFromDate(new Date('2026-01-02T10:00:00Z')),
		pendingEmail: pending,
		pendingExpireTime: pending ? timestampFromDate(new Date('2026-11-27T13:00:00Z')) : undefined,
		emailVerification: true
	});
}

describe('AccountSettings', () => {
	beforeEach(() => {
		switchLocale('en');
		vi.spyOn(sessionState, 'refresh').mockResolvedValue(undefined as never);
	});
	afterEach(() => {
		vi.restoreAllMocks();
		if (stepUpState.open) stepUpState.finish(false);
	});

	// V1-17: the name comes from the user and is text, never markup.
	it('shows the account as text', async () => {
		vi.spyOn(accountApi.account, 'getAccount').mockResolvedValue(account());
		render(AccountSettings);
		await expect.element(page.getByText('alice@example.org')).toBeVisible();
		await expect.element(page.getByLabelText('Display name')).toHaveValue(hostile);
		expect(document.querySelector('img[src="x"]')).toBeNull();
		await expect.element(page.getByText(/SerMon sends a code to the new address/)).toBeVisible();
	});

	it('changes the name after a step-up', async () => {
		vi.spyOn(accountApi.account, 'getAccount').mockResolvedValue(account());
		const rename = vi
			.spyOn(accountApi.account, 'setDisplayName')
			.mockRejectedValueOnce(stepUpError())
			.mockResolvedValue({} as never);
		render(AccountSettings);
		await page.getByLabelText('Display name').fill('  Alice B.  ');
		await page.getByRole('button', { name: 'Save name' }).click();
		await vi.waitFor(() => expect(stepUpState.open).toBe(true));
		stepUpState.finish(true);
		await expect.element(page.getByText('The name is saved.')).toBeVisible();
		expect(rename).toHaveBeenLastCalledWith({ displayName: 'Alice B.' });
	});

	it('asks for the mailed code and says when it is wrong', async () => {
		const get = vi.spyOn(accountApi.account, 'getAccount').mockResolvedValue(account());
		vi.spyOn(accountApi.account, 'requestEmailChange').mockResolvedValue({
			codeSent: true
		} as never);
		const confirm = vi
			.spyOn(accountApi.account, 'confirmEmailChange')
			.mockRejectedValueOnce(rpcError(Code.PermissionDenied))
			.mockResolvedValue({} as never);
		render(AccountSettings);
		await page.getByLabelText('New e-mail address').fill('alice@example.net');
		get.mockResolvedValue(account('alice@example.net'));
		await page.getByRole('button', { name: 'Change address' }).click();
		await expect
			.element(page.getByText('A code is on its way to alice@example.net.', { exact: false }))
			.toBeVisible();
		await expect.element(page.getByText(/Waiting for the code: alice@example.net/)).toBeVisible();

		await page.getByLabelText('Code from the e-mail').fill('abcd-efgh');
		await page.getByRole('button', { name: 'Confirm' }).click();
		await expect.element(page.getByText('The code is wrong.')).toBeVisible();
		get.mockResolvedValue(
			create(GetAccountResponseSchema, { ...account(), email: 'alice@example.net' })
		);
		await page.getByRole('button', { name: 'Confirm' }).click();
		await expect.element(page.getByText(/The new address applies/)).toBeVisible();
		expect(confirm).toHaveBeenLastCalledWith({ code: 'abcd-efgh' });
	});

	it('downloads the export after a step-up', async () => {
		vi.spyOn(accountApi.account, 'getAccount').mockResolvedValue(account());
		vi.spyOn(accountApi.account, 'exportAccountData').mockResolvedValue({
			json: '{"format":"x"}'
		} as never);
		const blob = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:x');
		render(AccountSettings);
		await page.getByRole('button', { name: 'Export data' }).click();
		await expect.element(page.getByText('The export is downloaded.')).toBeVisible();
		expect(blob).toHaveBeenCalled();
	});
});
