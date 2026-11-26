import AxeBuilder from '@axe-core/playwright';
import { installed, mockApi, type Handler } from './api';
import { expect, test } from './fixtures';

const web01 = '0193f5a0-0000-7000-8000-000000000001';
const db01 = '0193f5a0-0000-7000-8000-000000000002';
const app01 = '0193f5a0-0000-7000-8000-000000000003';
const hostile = '<img src=x onerror=alert(1)>';
const now = Date.now();

interface Query {
	selector?: { metric?: string; nodeIds?: string[]; labels?: Record<string, string> };
}

function points(value: number) {
	return [30, 20, 10].map((ago) => ({
		time: new Date(now - ago * 60_000).toISOString(),
		avg: value
	}));
}

function compareApi(seen: Query[] = []): Record<string, Handler> {
	return {
		...installed(true),
		'FleetService/GetFleetSummary': () => ({
			json: {
				total: 3,
				nodes: [
					{ nodeId: app01, hostname: 'app-01', online: false },
					{ nodeId: db01, hostname: hostile, online: true },
					{ nodeId: web01, hostname: 'web-01', online: true }
				]
			}
		}),
		'MetricsService/QueryMetrics': (body: unknown) => {
			const req = body as Query;
			seen.push(req);
			// app-01 has sent nothing in the range; CPU idle 80 % on web-01 and 40 % on db-01.
			const nodes = (req.selector?.nodeIds ?? []).filter((id) => id !== app01);
			const value = (id: string) =>
				req.selector?.metric === 'sermon_cpu_utilization_ratio'
					? id === web01
						? 800_000
						: 400_000
					: 100_000;
			return {
				json: {
					series: nodes.map((nodeId) => ({
						nodeId,
						labels: req.selector?.labels ?? {},
						points: points(value(nodeId))
					}))
				}
			};
		}
	};
}

test.describe('compare (mocked API)', () => {
	test('compares the nodes of the URL side by side and overlaid', async ({ page }) => {
		const seen: Query[] = [];
		await mockApi(page, compareApi(seen));
		await page.goto(`/compare?node=${web01}&node=${db01}`);

		await expect(page.getByRole('heading', { level: 1, name: 'Compare' })).toBeVisible();
		const views = page.getByRole('navigation', { name: 'Views of the fleet' });
		await expect(views.getByRole('link', { name: 'Compare' })).toHaveAttribute(
			'aria-current',
			'page'
		);
		// One query per metric for both nodes; CPU busy from idle.
		const cpu = page.getByRole('figure', { name: 'CPU busy · web-01' });
		await expect(cpu).toContainText('web-01: minimum 20%, maximum 20%, latest 20%');
		await expect(page.getByRole('figure', { name: /^CPU busy · <img/ })).toContainText(
			'maximum 60%'
		);
		const cpuQuery = seen.find((q) => q.selector?.metric === 'sermon_cpu_utilization_ratio');
		expect(cpuQuery?.selector?.nodeIds?.sort()).toEqual([web01, db01].sort());
		expect(cpuQuery?.selector?.labels).toEqual({ mode: 'idle' });
		expect(await page.locator('img[src="x"]').count()).toBe(0);
		expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);

		// Overlaid: one chart per metric; the view is part of the URL.
		await page.getByRole('button', { name: 'Overlaid' }).click();
		await expect(page).toHaveURL(/view=overlay/);
		const overlay = page.getByRole('figure', { name: 'CPU busy', exact: true });
		await expect(overlay).toContainText('web-01: minimum 20%');
		await expect(overlay).toContainText('maximum 60%');

		// A third node without data is named; the selection follows the URL.
		await page.getByRole('combobox', { name: 'Add node' }).selectOption({ label: 'app-01' });
		await expect(page).toHaveURL(new RegExp(`node=${app01}`));
		await expect(page.getByRole('note')).toContainText('No data in the time range: app-01');
		await page.screenshot({ path: 'test-results/compare.png', fullPage: true });
		expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);

		await page.getByRole('button', { name: 'Remove app-01' }).click();
		await page.getByRole('button', { name: 'Remove web-01' }).click();
		await expect(page.getByText('Choose at least 2 nodes to compare.')).toBeVisible();
		await expect(page.getByRole('figure')).toHaveCount(0);
	});

	test('leads from the node page into the comparison', async ({ page }) => {
		await mockApi(page, {
			...compareApi(),
			'NodeService/GetNode': () => ({
				json: { node: { id: web01, hostname: 'web-01', online: true } }
			}),
			'NodeService/ListFilesystems': () => ({ json: {} })
		});
		await page.goto(`/nodes/${web01}`);
		await page.getByRole('link', { name: 'Compare' }).click();
		await expect(page).toHaveURL(new RegExp(`/compare\\?node=${web01}$`));
		await expect(page.getByRole('list', { name: 'Compared nodes' })).toContainText('web-01');
	});
});
