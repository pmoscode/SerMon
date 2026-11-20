import AxeBuilder from '@axe-core/playwright';
import { installed, mockApi, session } from './api';
import { expect, test } from './fixtures';

const admin = { ...session, orgAdmin: true, enrollGroupIds: ['*'] };
const groups = [
	{ id: 'g1', name: 'web' },
	{ id: 'g2', name: 'db' }
];
const members = [
	{
		userId: session.userId,
		email: session.email,
		displayName: session.displayName,
		bindings: [{ id: 'b1', groupId: '', role: 'ROLE_ADMIN' }]
	},
	{
		userId: '0193f5a0-0000-7000-8000-0000000000a1',
		email: 'kai@example.org',
		displayName: 'Kai',
		bindings: [{ id: 'b2', groupId: 'g1', role: 'ROLE_VIEWER' }]
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

function adminApi(overrides: Parameters<typeof mockApi>[1] = {}) {
	return {
		...installed(true),
		'AuthService/GetSession': () => ({ json: { session: admin } }),
		'AccessService/ListNodeGroups': () => ({ json: { groups } }),
		'AccessService/ListMembers': () => ({ json: { members } }),
		'AccessService/ListInvitations': () => ({ json: {} }),
		'AccessService/ListAuditLog': () => ({
			json: {
				entries: [
					{
						seq: '2',
						time: new Date().toISOString(),
						actorId: session.userId,
						action: 'role.grant',
						target: 'user:kai',
						details: { role: 'viewer' }
					}
				]
			}
		}),
		'SecurityPolicyService/GetSecurityPolicy': () => ({ json: {} }),
		...overrides
	};
}

test.describe('organization admin (mocked API)', () => {
	test.use({ expectedHttpErrors: [400, 401] });

	test('shows the tabs to admins and invites after the step-up', async ({ page }) => {
		let confirmed = false;
		await mockApi(
			page,
			adminApi({
				'AccessService/InviteMember': () => (confirmed ? { json: { token: 'smi_link' } } : stepUp),
				'AuthService/ConfirmPassword': () => {
					confirmed = true;
					return { json: { session: admin } };
				}
			})
		);
		await page.goto('/admin');

		await expect(
			page.getByRole('navigation').getByRole('link', { name: 'Organization' })
		).toBeVisible();
		await expect(page.getByText('kai@example.org')).toBeVisible();
		await page.screenshot({ path: 'test-results/admin-members.png', fullPage: true });
		let results = await new AxeBuilder({ page }).analyze();
		expect(results.violations).toEqual([]);

		await page.getByRole('tab', { name: 'Invitations' }).click();
		await page.getByLabel('Email address').fill('new@example.org');
		await page.getByRole('button', { name: 'Invite' }).click();
		const dialog = page.getByRole('dialog');
		await expect(dialog).toBeVisible();
		results = await new AxeBuilder({ page }).analyze();
		expect(results.violations).toEqual([]);
		await dialog.getByLabel('Password').fill('correct horse battery');
		await dialog.getByRole('button', { name: 'Confirm' }).click();

		await expect(page.getByText(/\/invite#smi_link$/)).toBeVisible();
		await page.screenshot({ path: 'test-results/admin-invite.png', fullPage: true });

		for (const tab of ['Groups', 'Security', 'Audit log']) {
			await page.getByRole('tab', { name: tab }).click();
			results = await new AxeBuilder({ page }).analyze();
			expect(results.violations, tab).toEqual([]);
		}
		await expect(page.getByText('role.grant')).toBeVisible();
	});

	test('hides the organization from non-admins', async ({ page }) => {
		await mockApi(page, installed(true));
		await page.goto('/admin');

		await expect(
			page.getByRole('navigation').getByRole('link', { name: 'Organization' })
		).toHaveCount(0);
		await expect(page.getByRole('alert')).toHaveText(
			'You lack the rights for this (admin of all groups).'
		);
	});
});

test.describe('add node (mocked API)', () => {
	test('creates the command and waits for the node', async ({ page }) => {
		let tokenCreated = false;
		let pollsSinceToken = 0;
		const node = {
			nodeId: '0193f5a0-0000-7000-8000-000000000001',
			hostname: 'web-01',
			online: true,
			figures: { cpuBusyRatio: 0.1 }
		};
		await mockApi(
			page,
			adminApi({
				// The node shows up with the second poll after the token was created.
				'FleetService/GetFleetSummary': () => ({
					json: { nodes: tokenCreated && pollsSinceToken++ > 0 ? [node] : [] }
				}),
				'EnrollmentTokenService/CreateEnrollmentToken': () => {
					tokenCreated = true;
					return { json: { secret: 'sme_abc' } };
				}
			})
		);
		await page.goto('/fleet');
		await page.getByRole('link', { name: 'Add node' }).click();
		await expect(page).toHaveURL('/nodes/add');
		let results = await new AxeBuilder({ page }).analyze();
		expect(results.violations).toEqual([]);

		await page.getByRole('button', { name: 'Create command' }).click();
		await expect(page.locator('pre')).toContainText('SERMON_ENROLLMENT_TOKEN=sme_abc');
		await expect(page.getByRole('link', { name: 'View the script first' })).toHaveAttribute(
			'href',
			'/install.sh'
		);
		await expect(page.getByText('Connected: web-01')).toBeVisible({ timeout: 10_000 });
		await expect(page.getByText('First metrics received')).toBeVisible();
		await page.screenshot({ path: 'test-results/add-node.png', fullPage: true });
		results = await new AxeBuilder({ page }).analyze();
		expect(results.violations).toEqual([]);
	});
});

test.describe('invitation link (mocked API)', () => {
	test('creates an account and leads to the login', async ({ page }) => {
		let accepted: unknown;
		await mockApi(page, {
			...installed(false),
			'InvitationService/GetInvitation': () => ({
				json: { organizationName: 'Alpha GmbH', email: 'new@example.org', role: 'ROLE_OPERATOR' }
			}),
			'InvitationService/AcceptInvitation': (body) => {
				accepted = body;
				return { json: {} };
			}
		});
		await page.goto('/invite#smi_link');

		await expect(page.getByText('Alpha GmbH invites new@example.org as Operator.')).toBeVisible();
		const results = await new AxeBuilder({ page }).analyze();
		expect(results.violations).toEqual([]);
		await page.getByLabel('Name').fill('Neu');
		await page.getByLabel('Password', { exact: true }).fill('correct horse battery');
		await page.getByLabel('Repeat password').fill('correct horse battery');
		await page.getByRole('button', { name: 'Create account and join' }).click();

		await expect(page).toHaveURL('/login?reason=invited');
		await expect(page.getByRole('status')).toHaveText('Account created. Sign in now.');
		expect(accepted).toMatchObject({ token: 'smi_link', displayName: 'Neu' });
	});

	test('joins with the logged-in account', async ({ page }) => {
		await mockApi(page, {
			...installed(true),
			'InvitationService/GetInvitation': () => ({
				json: { organizationName: 'Beta AG', email: session.email, role: 'ROLE_VIEWER' }
			}),
			'InvitationService/JoinOrganization': () => ({
				json: { organizationId: session.organizations[1].id }
			}),
			'AuthService/SwitchOrganization': () => ({
				json: { session: { ...session, activeOrganizationId: session.organizations[1].id } }
			})
		});
		await page.goto('/invite#smi_link');

		await page.getByRole('button', { name: 'Join' }).click();
		await expect(page).toHaveURL('/fleet');
	});
});
