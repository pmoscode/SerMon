import AxeBuilder from '@axe-core/playwright';
import { installed, mockApi, session } from './api';
import { expect, test } from './fixtures';

const key = {
	ruleId: 'security.ssh.bruteforce',
	nodeId: '0193f5a0-0000-7000-8000-000000000001',
	dimension: 'ip=192.0.2.7'
};
const alerts = [
	{
		key,
		state: 'ALERT_STATE_CRITICAL',
		ruleName: { de: 'SSH-Brute-Force', en: 'SSH brute force' },
		dimensionLabels: { ip: '192.0.2.7' },
		hostname: 'web-01',
		groupId: 'g1',
		groupName: 'web',
		since: new Date(Date.now() - 300_000).toISOString(),
		value: 60,
		unit: 'count',
		fields: { user: 'root', ip: '192.0.2.7' },
		muted: true,
		canAcknowledge: true
	},
	{
		key: {
			ruleId: 'system.filesystem.full_soon',
			nodeId: '0193f5a0-0000-7000-8000-000000000002',
			dimension: 'mountpoint=/var'
		},
		state: 'ALERT_STATE_WARNING',
		ruleName: { de: 'Dateisystem bald voll', en: 'File system full soon' },
		dimensionLabels: { mountpoint: '/var' },
		hostname: 'db-01',
		groupId: 'g2',
		groupName: 'db',
		since: new Date(Date.now() - 3_600_000).toISOString(),
		value: 5.25,
		unit: 'hours',
		acknowledgedBy: 'Bob',
		acknowledgeTime: new Date(Date.now() - 600_000).toISOString()
	}
];

test.describe('alert page (mocked API)', () => {
	test('lists, filters, shows the history and acknowledges', async ({ page }) => {
		let acknowledged = false;
		const calls = await mockApi(page, {
			...installed(true),
			'AlertSilenceService/ListSilences': () => ({ json: {} }),
			'AlertRuleService/ListAlertRules': () => ({ json: {} }),
			'AlertService/ListAlerts': () => ({
				json: {
					alerts: acknowledged
						? [{ ...alerts[0], canAcknowledge: false, acknowledgedBy: 'Mia Muster' }, alerts[1]]
						: alerts
				}
			}),
			'AlertService/GetAlertHistory': () => ({
				json: {
					entries: [
						{
							kind: 'ALERT_HISTORY_KIND_STATE_CHANGED',
							from: 'ALERT_STATE_OK',
							to: 'ALERT_STATE_CRITICAL',
							value: 60,
							time: new Date().toISOString(),
							fields: { user: 'root' }
						}
					]
				}
			}),
			'AlertService/AcknowledgeAlert': (body) => {
				expect(body).toEqual({ key });
				acknowledged = true;
				return { json: {} };
			}
		});
		await page.goto('/alerts');

		const cards = page.getByRole('article');
		await expect(cards).toHaveCount(2);
		await expect(cards.first()).toContainText('SSH brute force');
		await expect(cards.first()).toContainText('Muted');
		await expect(cards.nth(1)).toContainText('Acknowledged by Bob');
		await page.getByRole('button', { name: 'History of SSH brute force on web-01' }).click();
		await expect(
			page.getByRole('region', { name: 'History of SSH brute force on web-01' })
		).toContainText('OK → Critical');
		await page.screenshot({ path: 'test-results/alerts.png', fullPage: true });
		const results = await new AxeBuilder({ page }).analyze();
		expect(results.violations).toEqual([]);

		await page.getByLabel('State').selectOption('pending');
		await expect(page.getByText('No alert matches the filter.')).toBeVisible();
		await page.getByRole('button', { name: 'Reset filter' }).click();
		await page.getByLabel('Group').selectOption('db');
		await expect(cards).toHaveCount(1);
		await page.getByLabel('Group').selectOption('');

		await page.getByRole('button', { name: 'Acknowledge SSH brute force on web-01' }).click();
		await expect(cards.first()).toContainText('Acknowledged by Mia Muster');
		expect(calls).toContain('AlertService/AcknowledgeAlert');

		await expect(page.getByRole('link', { name: 'web-01' })).toHaveAttribute(
			'href',
			'/nodes/0193f5a0-0000-7000-8000-000000000001'
		);
	});

	test('explains the quiet state and is reachable from the navigation', async ({ page }) => {
		await mockApi(page, {
			...installed(true),
			'AlertSilenceService/ListSilences': () => ({ json: {} }),
			'AlertRuleService/ListAlertRules': () => ({ json: {} }),
			'AlertService/ListAlerts': () => ({ json: {} })
		});
		await page.goto('/fleet');
		await page.getByRole('link', { name: 'Alerts' }).click();
		await expect(page.getByRole('heading', { name: 'No active alerts' })).toBeVisible();
		const results = await new AxeBuilder({ page }).analyze();
		expect(results.violations).toEqual([]);
	});
});

test.describe('rule editor (mocked API)', () => {
	test('shows the rules, checks an edit with a diff and saves', async ({ page }) => {
		const cpu =
			'apiVersion: sermon/v1\nmetadata:\n  id: system.cpu.usage_high\nspec:\n  window: 10m\n';
		const imports: unknown[] = [];
		await mockApi(page, {
			...installed(true),
			'AuthService/GetSession': () => ({ json: { session: { ...session, orgAdmin: true } } }),
			'AlertService/ListAlerts': () => ({ json: {} }),
			'AlertSilenceService/ListSilences': () => ({ json: {} }),
			'AlertRuleService/ListAlertRules': () => ({
				json: {
					rules: [
						{
							id: 'system.cpu.usage_high',
							name: { de: 'Hohe CPU-Auslastung', en: 'High CPU usage' },
							type: 'threshold',
							origin: 'ALERT_RULE_ORIGIN_DEFAULT',
							enabled: true,
							yaml: cpu,
							help: { de: 'Hilfe', en: 'The CPU was busy.' }
						}
					]
				}
			}),
			'AlertRuleService/ImportAlertRules': (body) => {
				imports.push(body);
				const dryRun = (body as { dryRun?: boolean }).dryRun === true;
				return {
					json: dryRun
						? {
								changes: [
									{
										ruleId: 'system.cpu.usage_high',
										action: 'ALERT_RULE_CHANGE_ACTION_UPDATE',
										beforeYaml: cpu,
										afterYaml: cpu.replace('10m', '15m')
									}
								]
							}
						: { applied: true }
				};
			}
		});
		await page.goto('/alerts');
		await page.getByRole('tab', { name: 'Rules' }).click();
		await expect(page.getByText('High CPU usage')).toBeVisible();
		await page.getByRole('button', { name: 'Details' }).click();
		await expect(page.getByText('The CPU was busy.')).toBeVisible();

		await page.getByRole('button', { name: 'Edit' }).click();
		const yaml = page.getByLabel(/Rules as YAML/);
		await expect(yaml).toHaveValue(cpu);
		await yaml.fill(cpu.replace('10m', '15m'));
		await page.getByRole('button', { name: 'Check' }).click();
		await expect(
			page.getByRole('figure', { name: 'Changes to system.cpu.usage_high' })
		).toContainText('window: 15m');
		await page.screenshot({ path: 'test-results/rules.png', fullPage: true });
		const results = await new AxeBuilder({ page }).analyze();
		expect(results.violations).toEqual([]);
		await page.getByRole('button', { name: 'Save' }).click();
		await expect(page.getByRole('button', { name: 'Import' })).toBeVisible();
		expect(imports).toHaveLength(2);
	});

	test('shows the statistics of 7 and 30 days and passes axe (M3-19)', async ({ page }) => {
		const asked: unknown[] = [];
		await mockApi(page, {
			...installed(true),
			'AlertSilenceService/ListSilences': () => ({ json: {} }),
			'AlertRuleService/ListAlertRules': () => ({ json: {} }),
			'AlertService/ListAlerts': () => ({ json: { alerts } }),
			'AlertService/GetAlertStatistics': (body) => {
				asked.push(body);
				return {
					json: {
						totals: {
							firings: 12,
							acknowledged: 4,
							resolved: 10,
							open: 2,
							unacknowledged: 8,
							mtta: '300s',
							mttr: '5400s'
						},
						rules: [
							{
								ruleId: 'security.ssh.bruteforce',
								ruleName: { de: 'SSH-Brute-Force', en: 'SSH brute force' },
								figures: { firings: 9, open: 1, mttr: '120s' },
								nodes: 3
							}
						]
					}
				};
			}
		});
		await page.goto('/alerts');
		await page.getByRole('tab', { name: 'Statistics' }).click();
		await expect(page.getByRole('row').nth(1)).toContainText('SSH brute force');
		await expect(page.getByText('1 hr 30 min')).toBeVisible();
		expect(asked).toEqual([{ days: 7 }]);
		await page.getByRole('button', { name: '30 days' }).click();
		await expect.poll(() => asked.length).toBe(2);
		expect(asked[1]).toEqual({ days: 30 });
		const results = await new AxeBuilder({ page }).analyze();
		expect(results.violations).toEqual([]);
	});

	test('leads from an alert to the charts of its node around the time (M3-19)', async ({
		page
	}) => {
		const fired = new Date(Date.now() - 2 * 3600_000);
		await mockApi(page, {
			...installed(true),
			'AlertSilenceService/ListSilences': () => ({ json: {} }),
			'AlertRuleService/ListAlertRules': () => ({ json: {} }),
			'AlertService/ListAlerts': () => ({
				json: {
					alerts: [{ ...alerts[1], fireTime: fired.toISOString(), labels: { category: 'disk' } }]
				}
			}),
			'NodeService/GetNode': () => ({
				json: {
					node: { id: alerts[1].key.nodeId, hostname: 'db-01', groupName: 'db', online: true }
				}
			}),
			'NodeService/ListFilesystems': () => ({ json: {} }),
			'MetricsService/QueryMetrics': () => ({ json: {} })
		});
		await page.goto('/alerts');
		await page.getByRole('link', { name: 'Charts around the time' }).click();
		await expect(page).toHaveURL(/\/nodes\/.*\?from=.*&to=.*&tab=disk/);
		await expect(page.getByRole('tab', { name: 'Disks' })).toHaveAttribute('aria-selected', 'true');
		// The charts show the hour around the firing: zoomed, with the way back to the preset.
		await expect(page.getByRole('button', { name: /Reset zoom/ })).toBeVisible();
	});
});
