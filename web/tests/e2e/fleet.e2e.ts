import AxeBuilder from '@axe-core/playwright';
import { installed, mockApi, unauthenticated } from './api';
import { expect, test } from './fixtures';

const group = (id: string, name: string) => ({
	groupId: id,
	groupName: name,
	osId: 'debian',
	osVersion: '13'
});
const nodes = [
	{
		nodeId: '0193f5a0-0000-7000-8000-000000000001',
		hostname: 'web-01',
		...group('g1', 'web'),
		online: true,
		figures: {
			cpuBusyRatio: 0.23,
			memoryUsedRatio: 0.41,
			filesystemMaxUsedRatio: 0.55,
			loadPerCpu: 0.3,
			uptimeSeconds: 90000,
			networkReceiveBytesPerSecond: 125000,
			networkTransmitBytesPerSecond: 40000
		}
	},
	{
		nodeId: '0193f5a0-0000-7000-8000-000000000002',
		hostname: 'web-02',
		...group('g1', 'web'),
		online: true,
		figures: {
			cpuBusyRatio: 0.93,
			memoryUsedRatio: 0.5,
			filesystemMaxUsedRatio: 0.2,
			uptimeSeconds: 3600
		}
	},
	{
		nodeId: '0193f5a0-0000-7000-8000-000000000003',
		hostname: 'db-01',
		...group('g2', 'db'),
		online: false,
		lastSeenAt: new Date(Date.now() - 600_000).toISOString()
	}
];

const fleetApi = {
	...installed(true),
	'FleetService/GetFleetSummary': () => ({ json: { total: 3, online: 2, offline: 1, nodes } }),
	'MetricsService/QueryMetrics': () => ({
		json: {
			series: [
				{ nodeId: nodes[0].nodeId, points: [{ avg: 800000 }, { avg: 700000 }, { avg: 750000 }] }
			]
		}
	})
};

test.describe('fleet overview (mocked API)', () => {
	test('shows tiles with status, filters and a table', async ({ page }) => {
		await mockApi(page, fleetApi);
		await page.goto('/fleet');

		const tiles = page.getByRole('article');
		await expect(tiles).toHaveCount(3);
		await expect(tiles.first()).toContainText('web-02');
		await expect(tiles.first()).toContainText('Critical');
		await expect(page.getByRole('article').filter({ hasText: 'db-01' })).toContainText(
			'Last seen 10 minutes ago'
		);
		await page.screenshot({ path: 'test-results/fleet-tiles.png', fullPage: true });
		let results = await new AxeBuilder({ page }).analyze();
		expect(results.violations).toEqual([]);

		await page.getByRole('button', { name: /Offline/ }).click();
		await expect(tiles).toHaveCount(1);
		await page.getByRole('button', { name: /Nodes/ }).click();
		await page.getByLabel('Search').fill('web-01');
		await expect(tiles).toHaveCount(1);
		await page.getByLabel('Search').fill('');
		await page.getByLabel('Group').selectOption('db');
		await expect(tiles).toHaveCount(1);
		await page.getByLabel('Group').selectOption('');

		await page.getByRole('button', { name: 'Table' }).click();
		await expect(page.getByRole('table')).toBeVisible();
		await expect(page.getByRole('row')).toHaveCount(4);
		await page.screenshot({ path: 'test-results/fleet-table.png', fullPage: true });
		results = await new AxeBuilder({ page }).analyze();
		expect(results.violations).toEqual([]);

		await page.reload();
		await expect(page.getByRole('table')).toBeVisible();
	});

	test('explains the empty fleet', async ({ page }) => {
		await mockApi(page, {
			...installed(true),
			'FleetService/GetFleetSummary': () => ({ json: { total: 0 } })
		});
		await page.goto('/fleet');

		await expect(page.getByRole('heading', { name: 'No nodes yet' })).toBeVisible();
	});

	test('leads to the login when the session ended', async ({ page }) => {
		await mockApi(page, {
			...installed(true),
			'FleetService/GetFleetSummary': () => unauthenticated
		});
		await page.goto('/fleet');

		await expect(page).toHaveURL('/login?reason=expired');
		await expect(page.getByRole('status')).toHaveText(
			'Your session has expired. Please sign in again.'
		);
	});
});
