import AxeBuilder from '@axe-core/playwright';
import { installed, mockApi, session } from './api';
import { expect, test } from './fixtures';

// M5-07: the modules – suggested in the fleet and at the node, set up per group by an admin
// after the step-up; axe on every view.
const admin = { ...session, orgAdmin: true, enrollGroupIds: ['*'] };
const group = '0193f5a0-0000-7000-8000-0000000000aa';
const nodeId = '0193f5a0-0000-7000-8000-000000000001';
const schema = JSON.stringify({
	type: 'object',
	additionalProperties: false,
	properties: {
		behavior: {
			type: 'string',
			enum: ['ok', 'fail'],
			maxLength: 8,
			default: 'ok',
			title: 'Behavior'
		},
		value: { type: 'integer', minimum: 0, maximum: 1000, default: 1, title: 'Value' },
		token: { type: 'string', writeOnly: true, maxLength: 64, title: 'Token' }
	}
});
const stepUp = {
	status: 400,
	json: {
		code: 'failed_precondition',
		message: 'please confirm your identity again',
		details: [{ type: 'sermon.web.v1.StepUpRequired', value: '' }]
	}
};

function moduleApi(saved: unknown[], confirmed: { done: boolean }) {
	return {
		...installed(true),
		'AuthService/GetSession': () => ({ json: { session: admin } }),
		'AuthService/ConfirmPassword': () => {
			confirmed.done = true;
			return { json: { session: admin } };
		},
		'AccessService/ListNodeGroups': () => ({ json: { groups: [{ id: group, name: 'web' }] } }),
		'AccessService/ListMembers': () => ({ json: {} }),
		'AccessService/ListInvitations': () => ({ json: {} }),
		'SecurityPolicyService/GetSecurityPolicy': () => ({ json: {} }),
		'AccessService/GetOrganizationQuotas': () => ({ json: {} }),
		'AccessService/ListAuditLog': () => ({ json: {} }),
		'MetricsService/QueryMetrics': () => ({ json: {} }),
		'ModuleService/ListModules': () => ({
			json: { modules: [{ id: 'simulate', schemaJson: schema, secretFields: ['token'] }] }
		}),
		'ModuleService/GetModuleSettings': () => ({
			json: {
				version: '3',
				groups: [{ groupId: group, groupName: 'web', available: ['simulate'] }]
			}
		}),
		'ModuleService/SetGroupModule': (body: unknown) => {
			if (!confirmed.done) return stepUp;
			if (JSON.stringify(body).includes('2000'))
				return {
					status: 400,
					json: {
						code: 'invalid_argument',
						message: 'simulate: value: 2000 is not within 0 to 1000'
					}
				};
			saved.push(body);
			return { json: {} };
		},
		'ModuleService/ListModuleHints': () => ({
			json: {
				hints: [
					{
						nodeId,
						hostname: 'web-01',
						groupId: group,
						hint: {
							moduleId: 'simulate',
							reasons: ['process:simulate'],
							suggestedConfigJson: '{"value":42}'
						}
					}
				]
			}
		}),
		'FleetService/GetFleetSummary': () => ({
			json: {
				total: 1,
				online: 1,
				nodes: [{ nodeId, hostname: 'web-01', groupId: group, groupName: 'web', online: true }]
			}
		})
	};
}

test.use({ expectedHttpErrors: [400, 401] });

test('suggests a module in the fleet and sets it up for the group after the step-up', async ({
	page
}) => {
	const saved: unknown[] = [];
	const confirmed = { done: false };
	await mockApi(page, moduleApi(saved, confirmed));
	await page.goto('/fleet');
	const hint = page.getByTestId('fleet-module-hint');
	await expect(hint).toContainText(
		'Test module (simulate) detected on web-01 – enable the module?'
	);
	let results = await new AxeBuilder({ page }).analyze();
	expect(results.violations, 'fleet with a suggestion').toEqual([]);

	await hint.getByRole('link', { name: 'Enable' }).click();
	await expect(page.getByRole('tab', { name: 'Modules' })).toHaveAttribute('aria-selected', 'true');
	await expect(page.getByLabel('Value')).toHaveValue('42');
	await expect(page.getByLabel(/^Token/)).toHaveAttribute('type', 'password');
	results = await new AxeBuilder({ page }).analyze();
	expect(results.violations, 'open form').toEqual([]);

	await page.getByLabel('Value').fill('2000');
	await page.getByRole('button', { name: 'Save' }).click();
	const dialog = page.getByRole('dialog');
	await dialog.getByLabel('Password').fill('correct horse battery');
	await dialog.getByRole('button', { name: 'Confirm' }).click();
	// The server checks with the agent's rules and names the property.
	await expect(
		page.getByText('Refused: simulate: value: 2000 is not within 0 to 1000')
	).toBeVisible();
	await page.getByLabel('Value').fill('7');
	await page.getByLabel(/^Token/).fill('typed-token'); // betterleaks:allow
	await page.getByRole('button', { name: 'Save' }).click();
	await expect.poll(() => saved.length).toBe(1);
	expect(saved[0]).toEqual({
		groupId: group,
		setting: { moduleId: 'simulate', enabled: true, configJson: '{"value":7}' },
		secrets: { token: 'typed-token' }
	});
	// The field is empty again and says nothing about the value.
	await expect(page.getByLabel(/^Token/)).toHaveCount(0);
});

// V1-25: an admin dismisses a suggestion in the fleet for its group and shows it again in the
// settings of the group's modules.
test('dismisses a suggestion for the group and shows it again', async ({ page }) => {
	const calls: unknown[] = [];
	await mockApi(page, {
		...moduleApi([], { done: true }),
		'ModuleService/SetModuleHintDismissed': (body: unknown) => {
			calls.push(body);
			return { json: {} };
		},
		'ModuleService/GetModuleSettings': () => ({
			json: {
				version: '3',
				groups: [
					{
						groupId: group,
						groupName: 'web',
						available: ['simulate'],
						dismissedHints: calls.length === 1 ? ['simulate'] : []
					}
				]
			}
		})
	});
	await page.goto('/fleet');
	await page
		.getByRole('button', {
			name: 'Dismiss the Test module (simulate) suggestion for the group of web-01'
		})
		.click();
	await expect(page.getByTestId('fleet-module-hint')).toHaveCount(0);
	expect(calls).toEqual([{ groupId: group, moduleId: 'simulate', dismissed: true }]);

	await page.goto('/admin');
	await page.getByRole('tab', { name: 'Modules' }).click();
	const dismissed = page.getByTestId('dismissed-hints');
	await expect(dismissed).toContainText('Dismissed suggestions');
	const results = await new AxeBuilder({ page }).analyze();
	expect(results.violations, 'dismissed suggestions').toEqual([]);
	await dismissed.getByRole('button', { name: 'Show again: Test module (simulate) (web)' }).click();
	await expect(dismissed).toHaveCount(0);
	expect(calls[1]).toEqual({ groupId: group, moduleId: 'simulate' });
});

test('shows the modules of a node with their state and passes axe', async ({ page }) => {
	await mockApi(page, {
		...moduleApi([], { done: true }),
		'NodeService/GetNode': () => ({
			json: {
				node: { id: nodeId, hostname: 'web-01', groupId: group, groupName: 'web', online: true }
			}
		}),
		'NodeService/ListFilesystems': () => ({ json: {} }),
		'MetricsService/QueryMetrics': () => ({ json: {} }),
		'MetricsService/ListMetrics': () => ({
			json: {
				metrics: [
					{
						name: 'sermon_simulate_value',
						unit: 'count',
						aggregation: 'last',
						area: 'module',
						module: 'simulate'
					}
				]
			}
		}),
		'ModuleService/GetNodeModules': () => ({
			json: {
				groupId: group,
				builtin: ['simulate'],
				settings: [{ moduleId: 'simulate', enabled: true, configJson: '{}' }],
				health: [
					{
						moduleId: 'simulate',
						state: 'MODULE_STATE_ERROR',
						reason: '<script>alert(1)</script> simulated failure'
					}
				],
				// A suggestion next to the running module: its reasons in readable contrast (M5-32).
				hints: [
					{
						moduleId: 'nginx',
						reasons: ['process:nginx', 'unit:nginx.service', 'port:80'],
						suggestedConfigJson: '{"url":"http://127.0.0.1/nginx_status"}'
					}
				]
			}
		})
	});
	await page.goto(`/nodes/${nodeId}?tab=modules`);
	await expect(page.getByTestId('module-state')).toHaveText('Error');
	await expect(page.getByText('<script>alert(1)</script> simulated failure')).toBeVisible();
	await expect(page.getByTestId('module-hint')).toContainText(
		'Seen: process:nginx, unit:nginx.service, port:80'
	);
	expect(await page.locator('main script').count()).toBe(0);
	const results = await new AxeBuilder({ page }).analyze();
	expect(results.violations).toEqual([]);
});
