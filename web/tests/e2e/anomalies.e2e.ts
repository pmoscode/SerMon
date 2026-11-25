import AxeBuilder from '@axe-core/playwright';
import { gzipSync } from 'node:zlib';
import { installed, mockApi, type Handler } from './api';
import { expect, test } from './fixtures';

const web01 = '0193f5a0-0000-7000-8000-000000000001';
const db01 = '0193f5a0-0000-7000-8000-000000000002';
const app01 = '0193f5a0-0000-7000-8000-000000000003';
const now = Date.now();
const hostile = '<img src=x onerror=alert(1)>\u202Elacol';

const fleetNode = (nodeId: string, hostname: string) => ({
	nodeId,
	hostname,
	groupId: 'g1',
	groupName: 'web',
	online: true,
	status: 'NODE_STATUS_OK',
	lastSeenAt: new Date(now - 5000).toISOString()
});

function rate(nodeId: string, ...ppm: number[]) {
	return {
		nodeId,
		points: ppm.map((avg, i) => ({
			time: new Date(now - (ppm.length - i) * 60_000).toISOString(),
			avg
		}))
	};
}

function anomaliesApi(): Record<string, Handler> {
	return {
		...installed(true),
		'FleetService/GetFleetSummary': () => ({
			json: {
				total: 3,
				online: 3,
				nodes: [fleetNode(web01, 'web-01'), fleetNode(db01, hostile), fleetNode(app01, 'app-01')]
			}
		}),
		'MetricsService/QueryMetrics': () => ({
			json: { series: [rate(web01, 20_000, 40_000), rate(db01, 300_000, 180_000)] }
		}),
		'AnomalyService/ListUnusual': () => ({
			json: {
				series: [
					{
						nodeId: db01,
						metric: 'sermon_disk_await_seconds',
						labels: { device: 'vda' },
						maxScore: 17.25,
						peak: new Date(now - 900_000).toISOString(),
						anomalousMinutes: 14
					}
				]
			}
		})
	};
}

test.describe('anomalies (mocked API)', () => {
	test('ranks the nodes by anomaly rate and lists what was unusual', async ({ page }) => {
		await mockApi(page, anomaliesApi());
		await page.goto('/anomalies');

		await expect(page.getByRole('heading', { level: 1, name: 'Anomalies' })).toBeVisible();
		// A view of the fleet (M4-25): its sub-navigation, "Fleet" stays the current item on top.
		const views = page.getByRole('navigation', { name: 'Views of the fleet' });
		await expect(views.getByRole('link', { name: 'Anomalies' })).toHaveAttribute(
			'aria-current',
			'page'
		);
		await expect(page.getByRole('link', { name: 'Fleet', exact: true })).toHaveAttribute(
			'aria-current',
			'page'
		);
		const rows = page.getByRole('table', { name: 'Nodes by anomaly rate' }).getByRole('row');
		// The highest first; app-01 has no rate (learning).
		await expect(rows).toHaveCount(3);
		await expect(rows.nth(1)).toContainText('18%');
		await expect(rows.nth(1)).toContainText('high');
		await expect(rows.nth(1)).toContainText('30%');
		await expect(rows.nth(2)).toContainText('web-01');
		await expect(rows.nth(2)).not.toContainText('high');
		await expect(page.getByText('Fleet anomaly rate: 11%')).toBeVisible();
		// The host chose the name: shown as text, its controls visible.
		await expect(rows.nth(1)).toContainText('<img src=x');
		expect(await page.locator('img[src="x"]').count()).toBe(0);

		const unusual = page.getByRole('table', { name: 'What was unusual?' });
		await expect(unusual).toContainText('device=vda');
		await expect(unusual).toContainText('17.3');
		await expect(unusual).toContainText('14');
		await page.screenshot({ path: 'test-results/anomalies.png', fullPage: true });
		expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
	});

	test('explains that nodes learn first', async ({ page }) => {
		await mockApi(page, {
			...anomaliesApi(),
			'MetricsService/QueryMetrics': () => ({ json: { series: [] } }),
			'AnomalyService/ListUnusual': () => ({ json: {} })
		});
		await page.goto('/anomalies');
		await expect(page.getByText(/new nodes learn for 7 days/)).toBeVisible();
		await expect(page.getByText('Nothing unusual in this range.')).toBeVisible();
	});

	test('the anomalies page loads less than 200 KiB of JavaScript', async ({ page }) => {
		await mockApi(page, anomaliesApi());
		const sizes = new Map<string, number>();
		page.on('response', async (response) => {
			if (response.url().endsWith('.js'))
				sizes.set(response.url(), gzipSync(await response.body()).length);
		});
		await page.goto('/anomalies');
		await page.waitForLoadState('networkidle');
		const size = [...sizes.values()].reduce((a, b) => a + b, 0);
		console.log(`anomalies: ${(size / 1024).toFixed(1)} KiB gzip`);
		expect(size).toBeLessThan(190 * 1024);
	});
});
