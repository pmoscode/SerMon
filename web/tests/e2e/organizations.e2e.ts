import AxeBuilder from '@axe-core/playwright';
import { installed, mockApi, session } from './api';
import { expect, test } from './fixtures';

const superadmin = { ...session, superadmin: true };
const organizations = [
	{
		id: '0193f5a0-0000-7000-8000-00000000000a',
		name: 'Alpha GmbH',
		createTime: '2026-11-01T10:00:00Z',
		memberCount: '4',
		nodeCount: '12',
		storageBytes: String(3 * 1024 ** 3),
		status: 'ORGANIZATION_STATUS_ACTIVE',
		quotas: { maxNodes: 20 }
	},
	{
		id: '0193f5a0-0000-7000-8000-00000000000b',
		name: 'Beta AG',
		createTime: '2026-11-20T10:00:00Z',
		memberCount: '1',
		nodeCount: '0',
		storageBytes: '0',
		status: 'ORGANIZATION_STATUS_ACTIVE',
		quotas: {}
	}
];
const stepUp = {
	status: 400,
	json: {
		code: 'failed_precondition',
		message: 'please confirm your identity again',
		details: [{ type: 'sermon.web.v1.StepUpRequired', value: '' }]
	}
};

// M2-32: the superadmin manages the organizations of the instance.
test.describe('organizations (mocked API)', () => {
	test.use({ expectedHttpErrors: [400] });

	test('lists, edits quotas after the step-up and creates an organization', async ({ page }) => {
		let confirmed = false;
		let updated: unknown;
		await mockApi(page, {
			...installed(true),
			'AuthService/GetSession': () => ({ json: { session: superadmin } }),
			'OrganizationService/ListOrganizations': () => ({ json: { organizations } }),
			'OrganizationService/UpdateOrganization': (body) => {
				if (!confirmed) return stepUp;
				updated = body;
				return { json: { organization: organizations[1] } };
			},
			'OrganizationService/CreateOrganization': () => ({
				json: { organization: organizations[1], invitationToken: 'smi_first' }
			}),
			'AuthService/ConfirmPassword': () => {
				confirmed = true;
				return { json: { session: superadmin } };
			}
		});
		await page.goto('/fleet');
		await page.getByRole('navigation').getByRole('link', { name: 'Organizations' }).click();

		const alpha = page.getByRole('row', { name: /Alpha GmbH/ });
		await expect(alpha).toContainText('12');
		await expect(alpha).toContainText('3 GiB');
		await expect(alpha).toContainText('Nodes: 20');
		await expect(page.getByRole('row', { name: /Beta AG/ })).toContainText('Default');
		let results = await new AxeBuilder({ page }).analyze();
		expect(results.violations).toEqual([]);
		await page.screenshot({ path: 'test-results/organizations.png', fullPage: true });

		await page.getByRole('button', { name: 'Edit Beta AG' }).click();
		const form = page.getByRole('form', { name: 'Edit Beta AG' });
		await form.getByLabel('Members (with open invitations)').fill('10');
		await form.getByRole('button', { name: 'Save' }).click();
		const dialog = page.getByRole('dialog');
		await expect(dialog).toBeVisible();
		await dialog.getByLabel('Password').fill('correct horse battery');
		await dialog.getByRole('button', { name: 'Confirm' }).click();
		await expect(page.getByRole('status').filter({ hasText: 'Saved.' })).toBeVisible();
		expect(updated).toMatchObject({
			organizationId: organizations[1].id,
			name: 'Beta AG',
			quotas: { maxMembers: 10 }
		});

		await page.getByLabel('Name', { exact: true }).fill('Gamma');
		await page.getByLabel('Email of the first admin').fill('gus@example.org');
		await page.getByRole('button', { name: 'Create' }).click();
		await expect(page.getByText(/\/invite#smi_first$/)).toBeVisible();
		results = await new AxeBuilder({ page }).analyze();
		expect(results.violations).toEqual([]);
	});

	test('is not there for others', async ({ page }) => {
		await mockApi(page, installed(true));
		await page.goto('/organizations');
		await expect(
			page.getByRole('navigation').getByRole('link', { name: 'Organizations' })
		).toHaveCount(0);
		await expect(page.getByRole('alert')).toHaveText('Superadmins only.');
	});
});
