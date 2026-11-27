import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { Code } from '#lib/api/client.js';
import { accountApi } from '#lib/api/account.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { rpcError } from '#lib/test/fakes.js';
import DeleteAccount from './DeleteAccount.svelte';

describe('DeleteAccount', () => {
	beforeEach(() => switchLocale('en'));
	afterEach(() => vi.restoreAllMocks());

	// V1-17: a deletion for good needs the address twice.
	it('deletes only when the address is typed twice', async () => {
		const del = vi
			.spyOn(accountApi.admin, 'deleteAccount')
			.mockResolvedValue({ memberships: 2 } as never);
		render(DeleteAccount);
		const button = page.getByRole('button', { name: 'Delete account' });
		await page.getByLabelText('E-mail address of the account').fill('bob@example.org');
		await page.getByLabelText('Repeat the address').fill('bob@example.com');
		await expect.element(button).toBeDisabled();
		await page.getByLabelText('Repeat the address').fill('BOB@example.org');
		await button.click();
		await expect
			.element(page.getByText('The account bob@example.org is deleted (2 memberships ended).'))
			.toBeVisible();
		expect(del).toHaveBeenCalledWith({ email: 'bob@example.org' });
	});

	it('explains a refusal', async () => {
		vi.spyOn(accountApi.admin, 'deleteAccount').mockRejectedValue(
			rpcError(Code.FailedPrecondition)
		);
		render(DeleteAccount);
		await page.getByLabelText('E-mail address of the account').fill('ann@example.org');
		await page.getByLabelText('Repeat the address').fill('ann@example.org');
		await page.getByRole('button', { name: 'Delete account' }).click();
		await expect.element(page.getByText(/belongs to a superadmin or the last admin/)).toBeVisible();
	});
});
