import { gzipSync } from 'node:zlib';
import type { Page } from '@playwright/test';
import { installed, mockApi } from './api';
import { expect, test } from './fixtures';

/** KONZEPT §17: the first load of a page needs less than 200 KiB of JavaScript (gzip). */
const BUDGET = 200 * 1024;
/**
 * M2-34: the pages keep 10 KiB of headroom, so a new feature notices before the budget breaks;
 * whatever opens on demand (popovers, dialogs, forms) loads on demand.
 */
const HEADROOM = 190 * 1024;

async function firstLoadJs(page: Page, path: string): Promise<number> {
	const sizes = new Map<string, number>();
	page.on('response', async (response) => {
		if (response.url().endsWith('.js'))
			sizes.set(response.url(), gzipSync(await response.body()).length);
	});
	await page.goto(path);
	await page.waitForLoadState('networkidle');
	return [...sizes.values()].reduce((a, b) => a + b, 0);
}

test('the login page loads less than 200 KiB of JavaScript', async ({ page }) => {
	await mockApi(page, installed(false));
	const size = await firstLoadJs(page, '/login');
	console.log(`login: ${(size / 1024).toFixed(1)} KiB gzip`);
	expect(size).toBeLessThan(BUDGET);
});

test('the fleet page loads less than 200 KiB of JavaScript', async ({ page }) => {
	await mockApi(page, installed(true));
	const size = await firstLoadJs(page, '/fleet');
	console.log(`fleet: ${(size / 1024).toFixed(1)} KiB gzip`);
	expect(size).toBeLessThan(BUDGET);
	expect(size, 'headroom of the fleet page (M2-34)').toBeLessThan(HEADROOM);
});

test('the node page with charts loads less than 200 KiB of JavaScript', async ({ page }) => {
	await mockApi(page, {
		...installed(true),
		'NodeService/GetNode': () => ({
			json: { node: { id: 'n1', hostname: 'web-01', online: true } }
		}),
		'NodeService/ListFilesystems': () => ({ json: {} }),
		'MetricsService/QueryMetrics': () => ({ json: {} })
	});
	const size = await firstLoadJs(page, '/nodes/0193f5a0-0000-7000-8000-000000000001');
	console.log(`node: ${(size / 1024).toFixed(1)} KiB gzip`);
	expect(size).toBeLessThan(BUDGET);
	expect(size, 'headroom of the node page (M2-34)').toBeLessThan(HEADROOM);
});

test('the log explorer loads less than 200 KiB of JavaScript', async ({ page }) => {
	await mockApi(page, {
		...installed(true),
		'LogService/QueryLogs': () => ({ json: {} }),
		'LogService/GetLogHistogram': () => ({ json: {} })
	});
	const size = await firstLoadJs(page, '/logs');
	console.log(`logs: ${(size / 1024).toFixed(1)} KiB gzip`);
	expect(size).toBeLessThan(BUDGET);
	expect(size, 'headroom of the log explorer (M2-34)').toBeLessThan(HEADROOM);
});

test('the process tab loads on demand and keeps the node page within the budget', async ({
	page
}) => {
	await mockApi(page, {
		...installed(true),
		'NodeService/GetNode': () => ({
			json: { node: { id: 'n1', hostname: 'web-01', online: true } }
		}),
		'NodeService/ListFilesystems': () => ({ json: {} }),
		'MetricsService/QueryMetrics': () => ({ json: {} })
	});
	// The stream stays open: the tab shows that it waits for the first snapshot.
	await page.route(/LiveViewService\/WatchProcesses$/, () => {});
	const first = await firstLoadJs(page, '/nodes/0193f5a0-0000-7000-8000-000000000001');
	const sizes = new Map<string, number>();
	page.on('response', async (response) => {
		if (response.url().endsWith('.js'))
			sizes.set(response.url(), gzipSync(await response.body()).length);
	});
	await page.getByRole('tab', { name: 'Processes (live)' }).click();
	await expect(page.getByText('Connecting to the agent …')).toBeVisible();
	await page.waitForLoadState('networkidle');
	const tab = [...sizes.values()].reduce((a, b) => a + b, 0);
	console.log(
		`node: ${(first / 1024).toFixed(1)} KiB gzip, process tab +${(tab / 1024).toFixed(1)} KiB`
	);
	expect(tab, 'the process tab is not part of the first load').toBeGreaterThan(0);
	expect(first + tab).toBeLessThan(BUDGET);
});

test('the security dashboard loads less than 200 KiB of JavaScript', async ({ page }) => {
	await mockApi(page, {
		...installed(true),
		'SecurityEventService/GetSecurityOverview': () => ({ json: {} }),
		'SecurityEventService/ListSecurityEvents': () => ({ json: {} })
	});
	const size = await firstLoadJs(page, '/security');
	console.log(`security: ${(size / 1024).toFixed(1)} KiB gzip`);
	expect(size).toBeLessThan(BUDGET);
	expect(size, 'headroom of the security dashboard (M2-34)').toBeLessThan(HEADROOM);
});
