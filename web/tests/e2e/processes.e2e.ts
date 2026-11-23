import AxeBuilder from '@axe-core/playwright';
import type { Page } from '@playwright/test';
import { envelopes, installed, mockApi } from './api';
import { expect, test } from './fixtures';

const nodeId = '0193f5a0-0000-7000-8000-000000000001';
const hostile = '<img src=x onerror=alert(1)><script>alert(2)</script>\u202Egpj.exe';

const nodeApi = {
	...installed(true),
	'NodeService/GetNode': () => ({
		json: { node: { id: nodeId, hostname: 'web-01', groupName: 'web', online: true } }
	}),
	'NodeService/ListFilesystems': () => ({ json: {} }),
	'MetricsService/QueryMetrics': () => ({ json: {} })
};

const snapshot = {
	snapshot: {
		takenAt: new Date().toISOString(),
		summary: {
			cpuBusyRatio: 0.25,
			cpuCount: 4,
			memoryTotalBytes: String(8 * 1024 ** 3),
			memoryAvailableBytes: String(6 * 1024 ** 3),
			swapTotalBytes: String(2 * 1024 ** 3),
			swapFreeBytes: String(2 * 1024 ** 3),
			load1: 0.5,
			load5: 0.4,
			load15: 0.3,
			uptimeSeconds: '90000',
			tasks: { total: 4, running: 1, sleeping: 3 }
		},
		processes: [
			{
				pid: 1,
				uid: 0,
				user: 'root',
				state: 'S',
				name: 'systemd',
				threads: 1,
				cpuRatio: 0.01,
				rssBytes: '12000000',
				memoryRatio: 0.003,
				runtimeSeconds: '90000',
				cmdline: '/sbin/init splash',
				unit: 'init.scope'
			},
			{
				pid: 812,
				ppid: 1,
				uid: 33,
				user: 'www-data',
				state: 'R',
				name: 'nginx',
				threads: 4,
				cpuRatio: 0.42,
				rssBytes: '48000000',
				memoryRatio: 0.012,
				runtimeSeconds: '3600',
				cmdline: 'nginx: worker process',
				unit: 'nginx.service'
			},
			{
				pid: 900,
				ppid: 812,
				uid: 33,
				user: 'www-data',
				state: 'S',
				name: hostile,
				threads: 1,
				cpuRatio: 0.2,
				cmdline: `${hostile} --password=***`,
				container: '0123456789ab'
			},
			{ pid: 2, state: 'S', name: 'kthreadd', threads: 1, kernelThread: true, cmdline: '' }
		],
		omitted: 12
	}
};

/** The live view answers with the messages once; later reconnects stay open. */
async function watchWith(page: Page, ...messages: unknown[]): Promise<() => number> {
	let calls = 0;
	await page.route(/\/sermon\.web\.v1\.LiveViewService\/WatchProcesses$/, async (route) => {
		calls++;
		if (calls > 1) return;
		await route.fulfill({
			status: 200,
			contentType: 'application/connect+json',
			body: envelopes(...messages)
		});
	});
	return () => calls;
}

test('the process tab streams only while open, shows host texts as text and passes axe', async ({
	page
}) => {
	await mockApi(page, nodeApi);
	const calls = await watchWith(page, snapshot);
	await page.goto(`/nodes/${nodeId}`);
	await expect(page.getByRole('heading', { name: 'web-01' })).toBeVisible();
	expect(calls()).toBe(0);

	await page.getByRole('tab', { name: 'Processes (live)' }).click();
	const table = page.getByRole('region', { name: 'Processes' });
	await expect(table.getByRole('button', { name: /Details of nginx/ })).toBeVisible();
	expect(calls()).toBe(1);
	await expect(page.getByText('2 GiB of 8 GiB')).toBeVisible();
	await expect(
		page.getByText('The agent left out 12 more processes', { exact: false })
	).toBeVisible();
	await expect(table.getByText('gpj.exe', { exact: false }).first()).toBeVisible();
	await expect(table.getByText('[kthreadd]')).toBeVisible();
	expect(await page.locator('img[src="x"]').count()).toBe(0);
	expect(await table.textContent()).not.toContain('\u202E');

	const results = await new AxeBuilder({ page }).analyze();
	expect(results.violations).toEqual([]);

	await page.getByRole('button', { name: /Details of .*gpj\.exe, PID 900/ }).click();
	const drawer = page.getByRole('dialog');
	await expect(drawer.locator('pre')).toHaveText(
		'<img src=x onerror=alert(1)><script>alert(2)</script>⟨U+202E⟩gpj.exe --password=***'
	);
	await expect(drawer.getByText('nginx, PID 812')).toBeVisible();
	await expect(drawer.getByText('n/a', { exact: true }).first()).toBeVisible();
	const drawerResults = await new AxeBuilder({ page }).include('[role="dialog"]').analyze();
	expect(drawerResults.violations).toEqual([]);
	await page.keyboard.press('Escape');
	await expect(drawer).toBeHidden();
	expect(await page.locator('img[src="x"]').count()).toBe(0);
});

test('sorting, filters, tree and pause work on the shown processes', async ({ page }) => {
	await mockApi(page, nodeApi);
	await watchWith(page, snapshot);
	await page.goto(`/nodes/${nodeId}`);
	await page.getByRole('tab', { name: 'Processes (live)' }).click();
	const table = page.getByRole('region', { name: 'Processes' });
	const firstName = table.locator('tbody tr:not([aria-hidden]) td:nth-child(10) button').first();
	await expect(firstName).toHaveText('nginx');

	await page.getByRole('button', { name: 'Name', exact: true }).click();
	await expect(firstName).toContainText('<img');
	await expect(page.getByRole('columnheader', { name: 'Name', exact: true })).toHaveAttribute(
		'aria-sort',
		'ascending'
	);

	await page.getByLabel('Regex').fill('worker\\s+process$');
	await expect(page.getByText('1 of 4 processes')).toBeVisible();
	await page.getByLabel('Regex').fill('(');
	await expect(page.getByRole('alert')).toContainText('Invalid regular expression');
	await page.getByLabel('Regex').fill('');

	await page.getByLabel('Tree view').check();
	await expect(firstName).toHaveText('[kthreadd]');
	await page.getByRole('button', { name: 'Pause' }).click();
	await expect(page.getByText(/^Paused – showing the state of/)).toBeVisible();
	await page.getByRole('button', { name: 'Resume' }).click();
	await expect(page.getByRole('button', { name: 'Pause' })).toBeVisible();
});

test('an offline agent and its refusal are explained', async ({ page }) => {
	await mockApi(page, nodeApi);
	// The first stream says offline and ends; the reconnect a second later gets the refusal.
	const answers = [[{ offline: true }], [{ refused: 'allow_live_view = false' }]];
	await page.route(/\/sermon\.web\.v1\.LiveViewService\/WatchProcesses$/, async (route) => {
		const messages = answers.shift();
		if (!messages) return;
		await route.fulfill({
			status: 200,
			contentType: 'application/connect+json',
			body: envelopes(...messages)
		});
	});
	await page.goto(`/nodes/${nodeId}`);
	await page.getByRole('tab', { name: 'Processes (live)' }).click();
	await expect(page.getByText(/not connected to this server instance/)).toBeVisible();
	await expect(page.getByRole('alert')).toContainText(
		'The agent refuses the live view: allow_live_view = false'
	);
	await expect(page.getByRole('button', { name: 'Try again' })).toBeVisible();
});
