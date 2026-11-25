import AxeBuilder from '@axe-core/playwright';
import { gzipSync } from 'node:zlib';
import { installed, mockApi, type Handler } from './api';
import { expect, test } from './fixtures';

const web01 = '0193f5a0-0000-7000-8000-000000000001';
const db01 = '0193f5a0-0000-7000-8000-000000000002';
const app01 = '0193f5a0-0000-7000-8000-000000000003';
const hostile = '<img src=x onerror=alert(1)>\u202Elacol';
const now = Date.now();
const columns = 60;

function values(f: (i: number) => number) {
	return Array.from({ length: columns }, (_, i) => f(i));
}

function heatmapApi(
	seen: { kind?: string; from?: string; to?: string }[] = []
): Record<string, Handler> {
	return {
		...installed(true),
		'HeatmapService/GetHeatmap': (body: unknown) => {
			const req = body as { kind?: string; from?: string; to?: string };
			seen.push(req);
			const memory = req.kind === 'HEATMAP_KIND_MEMORY';
			return {
				json: {
					start: new Date(Math.floor((now - 3600_000) / 60_000) * 60_000).toISOString(),
					step: '60s',
					columns,
					rows: [
						{ nodeId: app01, hostname: 'app-01', groupName: 'app', values: values(() => -1) },
						{
							nodeId: db01,
							hostname: hostile,
							groupName: 'db',
							values: values((i) => (i === 42 ? 0.97 : memory ? 0.2 : 0.4))
						},
						{
							nodeId: web01,
							hostname: 'web-01',
							groupName: 'web',
							values: values((i) => (memory ? 0.9 : i / columns))
						}
					]
				}
			};
		}
	};
}

test.describe('heatmap (mocked API)', () => {
	test('colours nodes × time, names cells by keyboard and offers a table', async ({ page }) => {
		const seen: { kind?: string; from?: string; to?: string }[] = [];
		await mockApi(page, heatmapApi(seen));
		await page.goto('/heatmap');

		await expect(page.getByRole('heading', { level: 1, name: 'Heatmap' })).toBeVisible();
		const views = page.getByRole('navigation', { name: 'Views of the fleet' });
		await expect(views.getByRole('link', { name: 'Heatmap' })).toHaveAttribute(
			'aria-current',
			'page'
		);
		const grid = page.getByRole('application', { name: /CPU of 3 nodes over time/ });
		await expect(grid).toBeVisible();
		expect(seen[0]).toMatchObject({ kind: 'HEATMAP_KIND_CPU' });
		await expect(page.getByLabel('Time range')).toHaveValue(String(86_400_000));
		// The last 24 hours at first.
		expect(Date.parse(seen[0].to!) - Date.parse(seen[0].from!)).toBe(86_400_000);

		// By name, the hostile name first ("<" sorts before letters); the cursor starts there.
		await grid.focus();
		await page.keyboard.press('Home');
		for (let i = 0; i < 42; i++) await page.keyboard.press('ArrowRight');
		await expect(page.getByRole('status').filter({ hasText: '97%' })).toContainText('<img src=x');
		expect(await page.locator('img[src="x"]').count()).toBe(0);
		await page.keyboard.press('ArrowDown');
		await expect(page.getByRole('status').filter({ hasText: 'app-01' })).toContainText('no data');
		// A shorter range is asked for anew.
		await page.getByLabel('Time range').selectOption({ label: '6 h' });
		await expect
			.poll(() => Date.parse(seen.at(-1)!.to!) - Date.parse(seen.at(-1)!.from!))
			.toBe(6 * 3600_000);
		await page.screenshot({ path: 'test-results/heatmap.png', fullPage: true });
		expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);

		// Another kind, the highest first, as a table.
		await page.getByRole('button', { name: 'Memory', exact: true }).click();
		await expect(page.getByRole('application', { name: /Memory of 3 nodes/ })).toBeVisible();
		expect(seen.at(-1)).toMatchObject({ kind: 'HEATMAP_KIND_MEMORY' });
		await page.getByLabel('Order').selectOption({ label: 'Mean' });
		await page.getByRole('button', { name: 'As a table' }).click();
		const rows = page.getByRole('table', { name: 'Memory per node' }).getByRole('row');
		await expect(rows).toHaveCount(4);
		await expect(rows.nth(1)).toContainText('web-01');
		await expect(rows.nth(1)).toContainText('90%');
		await expect(rows.nth(2)).toContainText('97%');
		await expect(rows.nth(3)).toContainText('app-01');
		await expect(rows.nth(3)).toContainText('–');
		expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
	});

	test('explains an empty fleet', async ({ page }) => {
		await mockApi(page, {
			...installed(true),
			'HeatmapService/GetHeatmap': () => ({ json: { columns: 60, step: '60s' } })
		});
		await page.goto('/heatmap');
		await expect(page.getByText('No nodes in this range.')).toBeVisible();
	});

	test('the heatmap page loads less than 200 KiB of JavaScript', async ({ page }) => {
		await mockApi(page, heatmapApi());
		const sizes = new Map<string, number>();
		page.on('response', async (response) => {
			if (response.url().endsWith('.js'))
				sizes.set(response.url(), gzipSync(await response.body()).length);
		});
		await page.goto('/heatmap');
		await page.waitForLoadState('networkidle');
		const size = [...sizes.values()].reduce((a, b) => a + b, 0);
		console.log(`heatmap: ${(size / 1024).toFixed(1)} KiB gzip`);
		expect(size).toBeLessThan(190 * 1024);
	});
});
