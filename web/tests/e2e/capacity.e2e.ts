import AxeBuilder from '@axe-core/playwright';
import { gzipSync } from 'node:zlib';
import { installed, mockApi, type Handler } from './api';
import { expect, test } from './fixtures';

const web01 = '0193f5a0-0000-7000-8000-000000000001';
const db01 = '0193f5a0-0000-7000-8000-000000000002';
const now = Date.now();
const day = 86_400_000;
const hostile = '<img src=x onerror=alert(1)>\u202Elacol';

/** 30 daily values from first to last. */
function days(first: number, last: number) {
	return Array.from({ length: 30 }, (_, i) => ({
		day: new Date(now - (30 - i) * day).toISOString(),
		value: first + ((last - first) * i) / 29
	}));
}

function capacityApi(filesystems = 3): Record<string, Handler> {
	const all = [
		{
			nodeId: db01,
			hostname: hostile,
			groupName: 'db',
			mountpoint: '/var/lib/postgresql',
			sizeBytes: String(100 * 1024 ** 3),
			availBytes: String(5 * 1024 ** 3),
			usedRatio: 0.95,
			fullAt: new Date(now + 3 * day + 60_000).toISOString()
		},
		{
			nodeId: web01,
			hostname: 'web-01',
			groupName: 'web',
			mountpoint: '/',
			sizeBytes: String(50 * 1024 ** 3),
			availBytes: String(20 * 1024 ** 3),
			usedRatio: 0.6
		},
		{
			nodeId: web01,
			hostname: 'web-01',
			groupName: 'web',
			mountpoint: '/mnt/iso',
			sizeBytes: String(4 * 1024 ** 3),
			availBytes: '0',
			usedRatio: 1,
			readonly: true
		}
	];
	for (let i = all.length; i < filesystems; i++)
		all.push({ ...all[1], mountpoint: `/data/${i}`, usedRatio: 0.1 });
	return {
		...installed(true),
		'CapacityService/GetCapacity': () => ({
			json: {
				filesystems: all,
				trends: [
					{
						nodeId: db01,
						hostname: hostile,
						groupName: 'db',
						cpu: days(0.2, 0.2),
						memory: days(0.5, 0.9)
					},
					{
						nodeId: web01,
						hostname: 'web-01',
						groupName: 'web',
						cpu: days(0.3, 0.7),
						memory: days(0.4, 0.4)
					}
				]
			}
		})
	};
}

test.describe('capacity (mocked API)', () => {
	test('lists the file systems by forecast and the top consumers', async ({ page }) => {
		await mockApi(page, capacityApi());
		await page.goto('/capacity');

		await expect(page.getByRole('heading', { level: 1, name: 'Capacity' })).toBeVisible();
		const views = page.getByRole('navigation', { name: 'Views of the fleet' });
		await expect(views.getByRole('link', { name: 'Capacity' })).toHaveAttribute(
			'aria-current',
			'page'
		);
		await expect(page.getByRole('link', { name: 'Fleet', exact: true })).toHaveAttribute(
			'aria-current',
			'page'
		);

		const fs = page.getByRole('table', { name: 'File systems by forecast' }).getByRole('row');
		await expect(fs).toHaveCount(4);
		await expect(fs.nth(1)).toContainText('/var/lib/postgresql');
		await expect(fs.nth(1)).toContainText(/full in 3 days/);
		await expect(fs.nth(2)).toContainText('not foreseeable');
		await expect(fs.nth(3)).toContainText('read-only');
		// The host chose the name: shown as text.
		await expect(fs.nth(1)).toContainText('<img src=x');
		expect(await page.locator('img[src="x"]').count()).toBe(0);

		const top = page.getByRole('table', { name: 'Top consumers' }).getByRole('row');
		// By CPU: web-01 (66 % in the last week, +32 points against the first) before db-01.
		await expect(top.nth(1)).toContainText('web-01');
		await expect(top.nth(1)).toContainText('+32%');
		await page.getByRole('button', { name: 'By memory' }).click();
		await expect(page.getByRole('button', { name: 'By memory' })).toHaveAttribute(
			'aria-pressed',
			'true'
		);
		await expect(top.nth(1)).toContainText('<img src=x');
		await page.screenshot({ path: 'test-results/capacity.png', fullPage: true });
		expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
	});

	test('shows 20 file systems first, the rest on request', async ({ page }) => {
		await mockApi(page, capacityApi(25));
		await page.goto('/capacity');
		const fs = page.getByRole('table', { name: 'File systems by forecast' }).getByRole('row');
		await expect(fs).toHaveCount(21);
		await page.getByRole('button', { name: 'Show all 25' }).click();
		await expect(fs).toHaveCount(26);
	});

	test('explains an empty fleet and a failure', async ({ page }) => {
		await mockApi(page, {
			...installed(true),
			'CapacityService/GetCapacity': () => ({ json: {} })
		});
		await page.goto('/capacity');
		await expect(page.getByText(/No file systems yet/)).toBeVisible();
		await expect(page.getByText('No daily values yet.')).toBeVisible();
	});

	test('the capacity page loads less than 200 KiB of JavaScript', async ({ page }) => {
		await mockApi(page, capacityApi());
		const sizes = new Map<string, number>();
		page.on('response', async (response) => {
			if (response.url().endsWith('.js'))
				sizes.set(response.url(), gzipSync(await response.body()).length);
		});
		await page.goto('/capacity');
		await page.waitForLoadState('networkidle');
		const size = [...sizes.values()].reduce((a, b) => a + b, 0);
		console.log(`capacity: ${(size / 1024).toFixed(1)} KiB gzip`);
		expect(size).toBeLessThan(190 * 1024);
	});
});
