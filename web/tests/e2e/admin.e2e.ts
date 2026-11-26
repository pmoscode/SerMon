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
		'AccessService/GetOrganizationQuotas': () => ({
			json: { quotas: { maxNodes: 20 }, nodes: '12', members: '2', openInvitations: '1' }
		}),
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

		for (const tab of ['Groups', 'Security', 'Quotas', 'Audit log']) {
			await page.getByRole('tab', { name: tab, exact: true }).click();
			results = await new AxeBuilder({ page }).analyze();
			expect(results.violations, tab).toEqual([]);
		}
		await expect(page.getByText('role.grant')).toBeVisible();
		await page.getByRole('tab', { name: 'Quotas' }).click();
		await expect(page.getByText('12 of 20')).toBeVisible();
	});

	// M3-12: the log settings tab – viewer switch with a step-up, a group's files, the state per
	// node with the agent's error as text; axe on the tab and the open form.
	test('sets the log settings after the step-up', async ({ page }) => {
		let confirmed = false;
		let viewersSeeLogs = true;
		const saved: unknown[] = [];
		await mockApi(
			page,
			adminApi({
				'LogSettingsService/GetLogSettings': () => ({
					json: {
						version: '3',
						viewersSeeLogs,
						groups: [
							{ groupId: 'g1', groupName: 'web', settings: { enabled: true } },
							{ groupId: 'g2', groupName: 'db', settings: { enabled: false }, custom: true }
						],
						redactionRules: [],
						nodes: [
							{ nodeId: 'n1', hostname: 'web-01', groupId: 'g1', appliedVersion: '3' },
							{
								nodeId: 'n2',
								hostname: 'db-01',
								groupId: 'g2',
								appliedVersion: '2',
								error: '<script>alert(1)</script> rule 1: look-around'
							}
						]
					}
				}),
				'LogSettingsService/SetViewerLogAccess': (body) => {
					if (!confirmed) return stepUp;
					viewersSeeLogs = (body as { viewersSeeLogs?: boolean }).viewersSeeLogs ?? false;
					return { json: {} };
				},
				'LogSettingsService/SetGroupLogSettings': (body) => {
					saved.push(body);
					return { json: {} };
				},
				'LogSettingsService/TestRedaction': () => ({ json: { redacted: 'password=***' } }),
				'AuthService/ConfirmPassword': () => {
					confirmed = true;
					return { json: { session: admin } };
				}
			})
		);
		await page.goto('/admin');
		await page.getByRole('tab', { name: 'Logs' }).click();
		await expect(page.getByText('Viewers see logs and security events.')).toBeVisible();
		await expect(page.getByRole('table')).toContainText('<script>alert(1)</script> rule 1');
		expect(await page.locator('table script').count()).toBe(0);
		let results = await new AxeBuilder({ page }).analyze();
		expect(results.violations).toEqual([]);

		await page.getByRole('button', { name: 'Hide from viewers' }).click();
		const dialog = page.getByRole('dialog');
		await dialog.getByLabel('Password').fill('correct horse battery');
		await dialog.getByRole('button', { name: 'Confirm' }).click();
		await expect(page.getByText('Viewers see no logs and security events.')).toBeVisible();

		await page.getByRole('button', { name: 'Edit the settings of web' }).click();
		await page.getByRole('button', { name: 'Add a pattern' }).click();
		await page.getByLabel('Pattern 1', { exact: true }).fill('/var/log/nginx/*.log');
		results = await new AxeBuilder({ page }).analyze();
		expect(results.violations, 'open form').toEqual([]);
		await page.getByRole('button', { name: 'Save', exact: true }).click();
		await expect.poll(() => saved.length).toBe(1);
		expect(saved[0]).toMatchObject({
			groupId: 'g1',
			settings: { enabled: true, files: [{ pattern: '/var/log/nginx/*.log' }] }
		});

		await page.getByLabel('Sample line to test').fill('password=hunter2');
		await page.getByRole('button', { name: 'Test', exact: true }).click();
		await expect(page.getByTestId('redaction-result')).toHaveText('password=***');
	});

	// M5-30: the organization's own retention, within the limits and the cap, after the step-up.
	test('sets the retention after the step-up', async ({ page }) => {
		let confirmed = false;
		let saved: unknown;
		await mockApi(
			page,
			adminApi({
				'AccessService/GetRetention': () => ({
					json: {
						eventRetentionDays: 90,
						logMinDays: 1,
						logMaxDays: 14,
						eventMinDays: 7,
						eventMaxDays: 365,
						capDays: 180
					}
				}),
				'AccessService/SetRetention': (body) => {
					if (!confirmed) return stepUp;
					saved = body;
					return { json: {} };
				},
				'AuthService/ConfirmPassword': () => {
					confirmed = true;
					return { json: { session: admin } };
				}
			})
		);
		await page.goto('/admin');
		await page.getByRole('tab', { name: 'Retention' }).click();
		const events = page.getByRole('spinbutton', {
			name: 'Security events and alert history (days)'
		});
		await expect(events).toHaveValue('90');
		await expect(page.getByText('Cap of this organization: 180 days')).toBeVisible();
		expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
		await page.getByRole('spinbutton', { name: 'Logs (days)' }).fill('3');
		await page.getByRole('button', { name: 'Save' }).click();
		const dialog = page.getByRole('dialog');
		await dialog.getByLabel('Password').fill('correct horse battery');
		await dialog.getByRole('button', { name: 'Confirm' }).click();
		await expect(page.getByText(/^Saved\./)).toBeVisible();
		expect(saved).toEqual({ logRetentionDays: 3, eventRetentionDays: 90 });
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

// M3-22: the groups of the provider as admins keep them, with the roles they gave in the list.
test.describe('SSO groups (mocked API)', () => {
	test.use({ expectedHttpErrors: [400, 401] });

	test('lists, creates after the step-up and passes axe', async ({ page }) => {
		let confirmed = false;
		const mappings = [
			{ id: 'm1', oidcGroup: 'CN=Ops,OU=Groups', groupId: 'g1', role: 'ROLE_OPERATOR' }
		];
		const created: unknown[] = [];
		await mockApi(
			page,
			adminApi({
				'AccessService/ListMembers': () => ({
					json: {
						members: [
							members[0],
							{
								...members[1],
								bindings: [{ id: 'b2', groupId: 'g1', role: 'ROLE_OPERATOR', fromOidc: true }]
							}
						]
					}
				}),
				'OidcMappingService/ListOidcMappings': () => ({
					json: { oidc: true, provisioning: true, mappings }
				}),
				'OidcMappingService/CreateOidcMapping': (body) => {
					if (!confirmed) return stepUp;
					created.push(body);
					mappings.push({ id: 'm2', oidcGroup: 'admins', groupId: '', role: 'ROLE_ADMIN' });
					return { json: { mapping: mappings[1] } };
				},
				'AuthService/ConfirmPassword': () => {
					confirmed = true;
					return { json: { session: admin } };
				}
			})
		);
		await page.goto('/admin');
		await expect(page.getByRole('listitem').filter({ hasText: 'kai@example.org' })).toContainText(
			'SSO'
		);
		await page.getByRole('tab', { name: 'SSO groups' }).click();
		await expect(page.getByRole('row').nth(1)).toContainText('CN=Ops,OU=Groups');
		await expect(page.getByRole('row').nth(1)).toContainText('web');
		let results = await new AxeBuilder({ page }).analyze();
		expect(results.violations).toEqual([]);

		await page.getByLabel('Group at the provider').fill('admins');
		await page
			.getByRole('tabpanel')
			.getByLabel('Role', { exact: true })
			.selectOption({ label: 'Admin' });
		await page.getByRole('button', { name: 'Create' }).click();
		const dialog = page.getByRole('dialog');
		await dialog.getByLabel('Password').fill('correct horse battery');
		await dialog.getByRole('button', { name: 'Confirm' }).click();
		await expect(page.getByRole('row').nth(2)).toContainText('All groups');
		expect(created).toEqual([{ oidcGroup: 'admins', role: 'ROLE_ADMIN' }]);
		results = await new AxeBuilder({ page }).analyze();
		expect(results.violations).toEqual([]);
	});
});
