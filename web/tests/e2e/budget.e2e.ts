import { gzipSync } from 'node:zlib';
import type { Page } from '@playwright/test';
import { installed, mockApi } from './api';
import { expect, test } from './fixtures';

/** KONZEPT §17: the first load of a page needs less than 200 KiB of JavaScript (gzip). */
const BUDGET = 200 * 1024;

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
});
