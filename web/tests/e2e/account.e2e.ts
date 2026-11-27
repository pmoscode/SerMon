import AxeBuilder from '@axe-core/playwright';
import { installed, mockApi } from './api';
import { expect, test } from './fixtures';

// V1-17: the own account – name, address with the mailed code, export – from the account menu.
test.describe('account (mocked API)', () => {
	test('changes the address with the mailed code and exports the data', async ({ page }) => {
		let email = 'mia@example.org';
		let pending = '';
		await mockApi(page, {
			...installed(true),
			'AccountService/GetAccount': () => ({
				json: {
					email,
					displayName: 'Mia',
					createTime: '2026-01-02T10:00:00Z',
					pendingEmail: pending,
					...(pending ? { pendingExpireTime: '2026-11-27T13:00:00Z' } : {}),
					emailVerification: true
				}
			}),
			'AccountService/RequestEmailChange': (body) => {
				pending = (body as { email: string }).email;
				return { json: { codeSent: true } };
			},
			'AccountService/ConfirmEmailChange': () => {
				email = pending;
				pending = '';
				return { json: {} };
			},
			'AccountService/ExportAccountData': () => ({
				json: { json: '{"format": "sermon-account-export/1"}' }
			})
		});
		await page.goto('/fleet');
		await page.getByRole('button', { name: 'Account' }).click();
		await page.getByRole('link', { name: 'Account', exact: true }).click();
		await expect(page).toHaveURL('/settings/account');
		await expect(page.getByRole('heading', { name: 'Account', level: 1 })).toBeVisible();
		const results = await new AxeBuilder({ page }).analyze();
		expect(results.violations).toEqual([]);

		await page.getByLabel('New e-mail address').fill('mia@example.net');
		await page.getByRole('button', { name: 'Change address' }).click();
		await expect(page.getByText(/A code is on its way to mia@example.net/)).toBeVisible();
		await page.getByLabel('Code from the e-mail').fill('ABCD-EFGH');
		await page.getByRole('button', { name: 'Confirm' }).click();
		await expect(page.getByText(/The new address applies/)).toBeVisible();
		await expect(page.getByText('mia@example.net', { exact: true })).toBeVisible();

		const download = page.waitForEvent('download');
		await page.getByRole('button', { name: 'Export data' }).click();
		expect((await download).suggestedFilename()).toBe('sermon-account.json');
	});
});
