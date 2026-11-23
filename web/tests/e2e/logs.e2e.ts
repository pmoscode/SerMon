import AxeBuilder from '@axe-core/playwright';
import type { Page } from '@playwright/test';
import { envelopes, installed, mockApi, type Handler } from './api';
import { expect, test } from './fixtures';

const web01 = '0193f5a0-0000-7000-8000-000000000001';
const db01 = '0193f5a0-0000-7000-8000-000000000002';
const now = Date.now();
const hostile = '<img src=x onerror=alert(1)><script>alert(2)</script> \u202Etxt.exe\u001b[31m red';

function line(msAgo: number, message: string, severity = 'LOG_SEVERITY_ERROR') {
	return {
		nodeId: web01,
		hostname: 'web-01',
		at: new Date(now - msAgo).toISOString(),
		severity,
		source: 'LOG_SOURCE_JOURNAL',
		unit: 'nginx.service',
		message,
		fields: { pid: '812', identifier: 'nginx' }
	};
}

const fleet = {
	nodes: [
		{ nodeId: web01, hostname: 'web-01', groupId: 'g1', groupName: 'web', online: true },
		{ nodeId: db01, hostname: 'db-01', groupId: 'g2', groupName: 'db', logsVetoed: true }
	]
};

function logApi(queries: unknown[] = []): Record<string, Handler> {
	return {
		...installed(true),
		'FleetService/GetFleetSummary': () => ({ json: fleet }),
		'MetricsService/QueryMetrics': () => ({ json: {} }),
		'LogService/QueryLogs': (body) => {
			queries.push(body);
			return {
				json: {
					lines: [line(1000, hostile), line(5000, 'worker started', 'LOG_SEVERITY_INFO')],
					more: true
				}
			};
		},
		'LogService/GetLogHistogram': () => ({
			json: {
				step: '36s',
				series: [
					{
						severity: 'LOG_SEVERITY_ERROR',
						points: [{ at: new Date(now - 60_000).toISOString(), count: 3 }]
					},
					{
						severity: 'LOG_SEVERITY_INFO',
						points: [{ at: new Date(now - 30_000).toISOString(), count: 5 }]
					}
				]
			}
		}),
		'LogService/GetLogContext': () => ({
			json: { lines: [line(9000, 'before the entry'), line(1000, hostile), line(500, 'after it')] }
		})
	};
}

async function tailWith(page: Page, ...messages: unknown[]) {
	let first = true;
	await page.route(/\/sermon\.web\.v1\.LogService\/TailLogs$/, async (route) => {
		if (!first) return; // later reconnects stay open until the test ends
		first = false;
		await route.fulfill({
			status: 200,
			contentType: 'application/connect+json',
			body: envelopes(...messages)
		});
	});
}

test('the log explorer shows hostile lines as text and passes axe', async ({ page }) => {
	await mockApi(page, logApi());
	await page.goto('/logs');
	await expect(page.getByRole('heading', { name: 'Logs', level: 1 })).toBeVisible();
	const lines = page.getByRole('region', { name: 'Lines' });
	await expect(lines.getByText('txt.exe', { exact: false })).toBeVisible();
	await expect(lines.getByText('⟨U+202E⟩', { exact: true })).toBeVisible();
	expect(await page.locator('img[src="x"]').count()).toBe(0);
	await expect(page.getByText(/allow_logs = false\): db-01/)).toBeVisible();
	await expect(page.getByRole('figure')).toContainText('Error and worse');

	const results = await new AxeBuilder({ page }).analyze();
	expect(results.violations).toEqual([]);
});

test('the filters live in the URL', async ({ page }) => {
	const queries: unknown[] = [];
	await mockApi(page, logApi(queries));
	await page.goto(`/logs?node=${web01}&severity=error&q=timed+out&icase=1&source=journal`);
	await expect(page.getByLabel('Node', { exact: true })).toHaveValue(web01);
	await expect(page.getByLabel('Severity')).toHaveValue('error');
	await expect(page.getByLabel('Text')).toHaveValue('timed out');
	await expect(page.getByLabel('Ignore case')).toBeChecked();
	await expect.poll(() => queries.length).toBeGreaterThan(0);
	expect((queries.at(-1) as { filter: Record<string, unknown> }).filter).toMatchObject({
		nodeIds: [web01],
		minSeverity: 'LOG_SEVERITY_ERROR',
		text: 'timed out',
		ignoreCase: true,
		sources: ['LOG_SOURCE_JOURNAL']
	});

	await page.getByRole('button', { name: '24 h' }).click();
	await expect(page).toHaveURL(/range=24h/);
	await page.getByLabel('Text').fill('refused');
	await page.getByLabel('Units').fill('nginx.service, cron');
	await page
		.getByRole('form', { name: 'Log filters' })
		.getByRole('button', { name: 'Search' })
		.click();
	await expect(page).toHaveURL(/q=refused/);
	await expect(page).toHaveURL(/unit=nginx.service&unit=cron/);
	await expect
		.poll(() => (queries.at(-1) as { filter: { text: string } }).filter.text)
		.toBe('refused');

	// Back in the history: the earlier filter again.
	await page.goBack();
	await expect(page.getByLabel('Text')).toHaveValue('timed out');
});

test('the context of an entry opens in a dialog', async ({ page }) => {
	await mockApi(page, logApi());
	await page.goto('/logs');
	await page
		.getByRole('button', { name: /Details of the line/ })
		.first()
		.click();
	await expect(page.getByText('identifier', { exact: true }).first()).toBeVisible();
	await page.getByRole('button', { name: 'Lines before and after' }).click();
	const dialog = page.getByRole('dialog');
	await expect(dialog.getByText('before the entry')).toBeVisible();
	await expect(dialog.locator('[aria-current="true"]')).toContainText('txt.exe');
	const results = await new AxeBuilder({ page }).include('[role="dialog"]').analyze();
	expect(results.violations).toEqual([]);
	await page.keyboard.press('Escape');
	await expect(dialog).toBeHidden();
});

test('live shows new lines on top and pauses', async ({ page }) => {
	await mockApi(page, logApi());
	await tailWith(page, {}, { lines: [line(100, 'fresh from the tail')] });
	await page.goto('/logs');
	await expect(page.getByText('worker started')).toBeVisible();
	await page.getByRole('button', { name: 'Live' }).click();
	const first = page.getByRole('region', { name: 'Lines' }).getByRole('listitem').first();
	await expect(first).toContainText('fresh from the tail');
	await page.getByRole('button', { name: 'Pause' }).click();
	await expect(page.getByText(/Paused: 0 new lines waiting/)).toBeVisible();
	await page.getByRole('button', { name: 'Stop live' }).click();
	await expect(page.getByRole('button', { name: 'Live' })).toBeVisible();
});

test('charts and the menu lead to the logs', async ({ page }) => {
	await mockApi(page, {
		...logApi(),
		'NodeService/GetNode': () => ({
			json: {
				node: { id: web01, hostname: 'web-01', groupId: 'g1', groupName: 'web', online: true }
			}
		}),
		'NodeService/ListFilesystems': () => ({ json: {} })
	});
	await page.goto(`/nodes/${web01}`);
	const link = page.getByRole('link', { name: 'Logs of this node in this range' }).first();
	await expect(link).toHaveAttribute('href', new RegExp(`/logs\\?from=.*&node=${web01}`));

	// The node's tab: its lines without host column, and the way to the explorer.
	await page.getByRole('tab', { name: 'Logs' }).click();
	await expect(
		page.getByRole('region', { name: 'Lines' }).getByText('worker started')
	).toBeVisible();
	await expect(page.getByLabel('Node', { exact: true })).toHaveCount(0);
	await page.getByRole('link', { name: 'Open in the log explorer' }).click();
	await expect(page).toHaveURL(new RegExp(`/logs\\?node=${web01}`));
	await expect(page.getByLabel('Node', { exact: true })).toHaveValue(web01);

	await page.getByRole('navigation').getByRole('link', { name: 'Logs' }).click();
	await expect(page).toHaveURL(/\/logs$/);
});
