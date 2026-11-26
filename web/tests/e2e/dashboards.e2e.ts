import AxeBuilder from '@axe-core/playwright';
import { installed, mockApi, type Handler } from './api';
import { expect, test } from './fixtures';

const me = '0193f5a0-0000-7000-8000-0000000000ff';
const web01 = '0193f5a0-0000-7000-8000-000000000001';
const db01 = '0193f5a0-0000-7000-8000-000000000002';
const group = '0193f5a0-0000-7000-8000-0000000000aa';
const boardId = '0193f5a0-0000-7000-8000-0000000000d1';
const hostile = '<img src=x onerror=alert(1)>';

interface Spec {
	version?: number;
	title?: string;
	widgets?: {
		id: string;
		position?: { x?: number; y?: number; w?: number; h?: number };
		[k: string]: unknown;
	}[];
	[k: string]: unknown;
}

/** A dashboard service in memory: it keeps what the editor saves. */
function dashboardApi(seen: Record<string, unknown[]>) {
	let board = {
		id: boardId,
		ownerId: me,
		ownerName: 'Mia Muster',
		revision: '1',
		canEdit: true,
		shared: false,
		spec: {
			version: 1,
			title: 'Web servers',
			timeRange: 'TIME_PRESET_6H',
			widgets: [
				{
					id: 'cpu',
					kind: 'WIDGET_KIND_TIMESERIES',
					title: 'CPU',
					position: { w: 6, h: 4 },
					query: { metric: 'sermon_cpu_utilization_ratio', target: { nodeIds: [web01] } }
				}
			]
		} as Spec
	};
	const record = (key: string, body: unknown) => (seen[key] ??= []).push(body);
	const handlers: Record<string, Handler> = {
		...installed(true),
		'DashboardService/ListDashboards': () => ({
			json: {
				dashboards: [
					{ ...board, spec: { version: 1, title: board.spec.title } },
					{
						id: '0193f5a0-0000-7000-8000-0000000000d2',
						ownerId: '0193f5a0-0000-7000-8000-0000000000ee',
						ownerName: hostile,
						shared: true,
						revision: '3',
						spec: { version: 1, title: hostile, description: 'Of a colleague' }
					}
				]
			}
		}),
		'DashboardService/GetDashboard': () => ({ json: { dashboard: board } }),
		'DashboardService/CreateDashboard': (body) => {
			record('create', body);
			board = { ...board, spec: (body as { spec: Spec }).spec, revision: '1' };
			return { json: { dashboard: board } };
		},
		'DashboardService/UpdateDashboard': (body) => {
			record('update', body);
			const req = body as { revision: string; spec: Spec };
			if (req.revision !== board.revision)
				return { status: 409, json: { code: 'aborted', message: 'changed meanwhile' } };
			board = { ...board, spec: req.spec, revision: String(Number(board.revision) + 1) };
			return { json: { dashboard: board } };
		},
		'DashboardService/SetDashboardShared': (body) => {
			record('share', body);
			board = { ...board, shared: (body as { shared?: boolean }).shared ?? false };
			return { json: { dashboard: board } };
		},
		'DashboardService/ExportDashboard': () => ({ json: { json: JSON.stringify(board.spec) } }),
		'DashboardService/ImportDashboard': (body) => {
			record('import', body);
			return { json: { dashboard: board } };
		},
		'MetricsService/ListMetrics': () => ({
			json: {
				metrics: [
					{
						name: 'sermon_cpu_utilization_ratio',
						unit: 'ratio_ppm',
						labels: ['mode'],
						area: 'cpu'
					},
					{ name: 'sermon_memory_utilization_ratio', unit: 'ratio_ppm', area: 'memory' }
				]
			}
		}),
		'MetricsService/QueryMetrics': () => ({ json: {} }),
		'FleetService/GetFleetSummary': () => ({
			json: {
				nodes: [
					{ nodeId: web01, hostname: 'web-01', groupId: group, groupName: 'web', online: true },
					{ nodeId: db01, hostname: hostile, groupId: group, groupName: 'web', online: true }
				]
			}
		})
	};
	return handlers;
}

test.describe('dashboards (mocked API)', () => {
	// 409: a save on an old revision (ABORTED).
	test.use({ expectedHttpErrors: [401, 409] });

	test('lists own and shared dashboards, names are text', async ({ page }) => {
		await mockApi(page, dashboardApi({}));
		await page.goto('/dashboards');
		await expect(page.getByRole('heading', { level: 1, name: 'Dashboards' })).toBeVisible();
		const views = page.getByRole('navigation', { name: 'Views of the fleet' });
		await expect(views.getByRole('link', { name: 'Dashboards' })).toHaveAttribute(
			'aria-current',
			'page'
		);
		const list = page.getByRole('list', { name: 'Dashboards' });
		await expect(list.getByRole('link', { name: 'Web servers' })).toBeVisible();
		await expect(list.getByText('by you')).toBeVisible();
		await expect(list.getByRole('link', { name: hostile })).toBeVisible();
		expect(await page.locator('img[src="x"]').count()).toBe(0);
		expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
	});

	test('creates a dashboard and builds it with the keyboard', async ({ page }) => {
		const seen: Record<string, unknown[]> = {};
		await mockApi(page, dashboardApi(seen));
		await page.goto('/dashboards');
		await page.getByLabel('Title of a new dashboard').fill('Database');
		await page.getByRole('button', { name: 'Create dashboard' }).click();
		await expect(page).toHaveURL(new RegExp(`/dashboards/${boardId}\\?edit=1$`));
		expect(seen.create?.[0]).toMatchObject({ spec: { version: 1, title: 'Database' } });

		// The editor: add a time series of web-01.
		await page.getByLabel('Kind of widget').selectOption({ label: 'Time series' });
		await page.getByRole('button', { name: 'Add widget' }).click();
		const form = page.getByRole('dialog', { name: 'Widget' });
		await form.getByLabel('Title').fill('Memory');
		await form.getByRole('button', { name: 'Apply' }).click();
		await expect(form.getByRole('alert')).toHaveText('Choose a metric.');
		await form.getByLabel('Metric').selectOption('sermon_memory_utilization_ratio');
		await form.getByLabel('web-01').check();
		expect(await page.locator('img[src="x"]').count()).toBe(0);
		expect(
			(await new AxeBuilder({ page }).include('[role="dialog"]').analyze()).violations
		).toEqual([]);
		await form.getByRole('button', { name: 'Apply' }).click();
		await expect(form).toBeHidden();

		const grid = page.getByRole('list', { name: 'Widgets of Database' });
		await expect(grid.getByRole('listitem', { name: 'Memory' })).toBeVisible();
		// Move it down by two rows and make it wider.
		await grid.getByRole('button', { name: 'Move or resize Memory' }).focus();
		await page.keyboard.press('ArrowDown');
		await page.keyboard.press('ArrowDown');
		await page.keyboard.press('Shift+ArrowRight');
		await expect(page.getByRole('status').filter({ hasText: 'Memory:' })).toHaveText(
			'Memory: column 1, row 3, 7 × 4'
		);
		expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
		await page.getByRole('button', { name: 'Save' }).click();
		await expect(page.getByRole('heading', { level: 1, name: 'Database' })).toBeVisible();
		const saved = seen.update?.[0] as { revision: string; spec: Spec };
		expect(saved.revision).toBe('1');
		expect(saved.spec.widgets).toEqual([
			expect.objectContaining({
				id: 'timeseries',
				title: 'Memory',
				position: { y: 2, w: 7, h: 4 },
				query: expect.objectContaining({
					metric: 'sermon_memory_utilization_ratio',
					target: { nodeIds: [web01] }
				})
			})
		]);
	});

	test('shares, exports and reports a conflicting change', async ({ page }) => {
		const seen: Record<string, unknown[]> = {};
		await mockApi(page, dashboardApi(seen));
		await page.goto(`/dashboards/${boardId}`);
		await expect(page.getByRole('heading', { level: 1, name: 'Web servers' })).toBeVisible();
		await expect(page.getByRole('button', { name: '6 h' })).toHaveAttribute('aria-pressed', 'true');
		await page.getByRole('button', { name: 'Share in the organization' }).click();
		await expect(page.getByRole('button', { name: 'Stop sharing' })).toBeVisible();
		expect(seen.share?.[0]).toMatchObject({ dashboardId: boardId, shared: true });

		const download = page.waitForEvent('download');
		await page.getByRole('button', { name: 'Export JSON' }).click();
		expect((await download).suggestedFilename()).toBe('web-servers.json');

		// Someone else saved meanwhile: the revision of the page is old.
		await page.getByRole('button', { name: 'Edit' }).click();
		await page.route(/DashboardService\/UpdateDashboard$/, (route) =>
			route.fulfill({
				status: 409,
				contentType: 'application/json',
				body: JSON.stringify({ code: 'aborted', message: 'changed meanwhile' })
			})
		);
		await page.getByRole('button', { name: 'Save' }).click();
		await expect(
			page.getByRole('alert').filter({ hasText: 'changed the dashboard meanwhile' })
		).toBeVisible();
	});

	test('imports a JSON file', async ({ page }) => {
		const seen: Record<string, unknown[]> = {};
		await mockApi(page, dashboardApi(seen));
		await page.goto('/dashboards');
		const json = JSON.stringify({ version: 1, title: 'Imported' });
		await page.getByLabel('Import JSON').setInputFiles({
			name: 'board.json',
			mimeType: 'application/json',
			buffer: Buffer.from(json)
		});
		await expect(page).toHaveURL(new RegExp(`/dashboards/${boardId}$`));
		expect(seen.import?.[0]).toEqual({ json });
	});
});
