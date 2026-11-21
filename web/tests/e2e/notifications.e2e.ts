import AxeBuilder from '@axe-core/playwright';
import { installed, mockApi, session } from './api';
import { expect, test } from './fixtures';

const admin = { ...session, orgAdmin: true, enrollGroupIds: ['*'], silenceGroupIds: ['*'] };
const SECRET = 'tk_e2e_fixture_secret'; // betterleaks:allow

const channelTypes = {
	types: [
		{
			type: 'ntfy',
			fields: [
				{ name: 'server', kind: 'CHANNEL_FIELD_KIND_URL', required: true, maxLength: 2048 },
				{
					name: 'topic',
					kind: 'CHANNEL_FIELD_KIND_TEXT',
					secret: true,
					required: true,
					maxLength: 64
				},
				{ name: 'token', kind: 'CHANNEL_FIELD_KIND_TEXT', secret: true, maxLength: 256 }
			]
		}
	]
};
const channel = {
	id: 'c1',
	type: 'ntfy',
	name: 'Ops',
	language: 'en',
	enabled: true,
	settings: { server: 'https://ntfy.example' },
	secretNames: ['token', 'topic']
};
const groups = { groups: [{ id: 'g1', name: 'web' }] };

test.describe('notification settings (mocked API)', () => {
	test('manages channels, policies and windows; secrets never come back', async ({ page }) => {
		let created: unknown;
		await mockApi(page, {
			...installed(true),
			'AuthService/GetSession': () => ({ json: { session: admin } }),
			'NotificationChannelService/ListChannelTypes': () => ({ json: channelTypes }),
			'NotificationChannelService/ListChannels': () => ({
				json: { channels: created ? [channel] : [] }
			}),
			'NotificationChannelService/CreateChannel': (body) => {
				created = body;
				return { json: { channel } };
			},
			'NotificationChannelService/TestChannel': () => ({
				json: {
					delivery: { status: 'DELIVERY_STATUS_FAILED', error: 'POST ntfy.example: HTTP 404' }
				}
			}),
			'NotificationChannelService/ListDeliveries': () => ({ json: {} }),
			'NotificationPolicyService/ListPolicies': () => ({ json: {} }),
			'MaintenanceWindowService/ListMaintenanceWindows': () => ({ json: {} }),
			'AccessService/ListNodeGroups': () => ({ json: groups })
		});
		await page.goto('/fleet');
		await page.getByRole('link', { name: 'Notifications' }).click();
		await expect(page.getByRole('heading', { name: 'Notifications' })).toBeVisible();

		await page.getByRole('button', { name: 'New channel' }).click();
		await page.getByLabel('Name').fill('Ops');
		await page.getByLabel('Server').fill('https://ntfy.example');
		await page.getByLabel('Topic').fill('sermon-ops');
		await page.getByLabel('Token (optional)').fill(SECRET);
		await page.getByRole('button', { name: 'Create channel' }).click();
		await expect(page.getByText('Set: Token, Topic')).toBeVisible();
		expect(created).toMatchObject({ secrets: { token: SECRET, topic: 'sermon-ops' } });
		await expect(page.locator('body')).not.toContainText(SECRET);

		await page.getByRole('button', { name: 'Send test' }).click();
		await expect(page.getByRole('alert')).toContainText('Test failed: POST ntfy.example: HTTP 404');
		let results = await new AxeBuilder({ page }).analyze();
		expect(results.violations).toEqual([]);

		await page.getByRole('tab', { name: 'Policies' }).click();
		await expect(page.getByRole('note')).toContainText('all alerts go to all enabled channels');
		await page.getByRole('button', { name: 'New policy' }).click();
		results = await new AxeBuilder({ page }).analyze();
		expect(results.violations).toEqual([]);

		await page.getByRole('tab', { name: 'Maintenance windows' }).click();
		await page.getByRole('button', { name: 'New maintenance window' }).click();
		await expect(page.getByLabel('Time zone')).toBeVisible();
		results = await new AxeBuilder({ page }).analyze();
		expect(results.violations).toEqual([]);

		await page.getByRole('tab', { name: 'Log' }).click();
		await expect(page.getByText('No deliveries yet.')).toBeVisible();
		await expect(page.getByRole('tab', { name: 'Log' })).toHaveAttribute('aria-selected', 'true');
		await expect(page.getByRole('tab', { name: 'Maintenance windows' })).toHaveAttribute(
			'aria-selected',
			'false'
		);
		await page.screenshot({ path: 'test-results/notifications.png', fullPage: true });
	});

	test('mutes an alert from the alert page', async ({ page }) => {
		let silence: unknown;
		await mockApi(page, {
			...installed(true),
			'AuthService/GetSession': () => ({
				json: { session: { ...session, silenceGroupIds: ['g1'] } }
			}),
			'AlertService/ListAlerts': () => ({
				json: {
					alerts: [
						{
							key: { ruleId: 'system.cpu.usage_high', nodeId: 'n1', dimension: '' },
							state: 'ALERT_STATE_WARNING',
							ruleName: { de: 'Hohe CPU-Auslastung', en: 'High CPU usage' },
							hostname: 'web-01',
							groupId: 'g1',
							groupName: 'web',
							since: new Date().toISOString(),
							value: 0.9,
							unit: 'ratio'
						}
					]
				}
			}),
			'FleetService/GetFleetSummary': () => ({
				json: { nodes: [{ nodeId: 'n1', hostname: 'web-01', groupId: 'g1', groupName: 'web' }] }
			}),
			'AlertSilenceService/ListSilences': () => ({ json: {} }),
			'AlertSilenceService/CreateSilence': (body) => {
				silence = body;
				return { json: { silence: { id: 's1' } } };
			}
		});
		await page.goto('/alerts');
		await page.getByRole('button', { name: 'Mute High CPU usage on web-01' }).click();
		await expect(page.getByRole('tab', { name: 'Silences' })).toHaveAttribute(
			'aria-selected',
			'true'
		);
		await expect(page.getByLabel('Rules')).toHaveValue('system.cpu.usage_high');
		await page.getByLabel('Comment (why?)').fill('Load test');
		const results = await new AxeBuilder({ page }).analyze();
		expect(results.violations).toEqual([]);
		await page.getByRole('button', { name: 'Mute', exact: true }).click();
		await expect
			.poll(() => silence)
			.toMatchObject({
				spec: {
					matchers: { groupIds: ['g1'], rules: ['system.cpu.usage_high'] },
					nodeIds: ['n1'],
					comment: 'Load test'
				}
			});
	});
});
