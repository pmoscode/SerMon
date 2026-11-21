import AxeBuilder from '@axe-core/playwright';
import { installed, mockApi } from './api';
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
			'AlertService/ListAlerts': () => ({ json: {} })
		});
		await page.goto('/fleet');
		await page.getByRole('link', { name: 'Alerts' }).click();
		await expect(page.getByRole('heading', { name: 'No active alerts' })).toBeVisible();
		const results = await new AxeBuilder({ page }).analyze();
		expect(results.violations).toEqual([]);
	});
});
