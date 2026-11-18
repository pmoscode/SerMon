import { test as base, expect } from '@playwright/test';

/**
 * Every E2E test runs with a guard that enforces KONZEPT D27 and the strict CSP:
 * - requests to any host other than sermon-server are blocked and fail the test,
 * - console errors (including CSP violations) and uncaught exceptions fail the test.
 */
export const test = base.extend<{ guard: void }>({
	guard: [
		async ({ page, baseURL }, use) => {
			const origin = new URL(baseURL ?? '').origin;
			const foreignRequests: string[] = [];
			const problems: string[] = [];

			await page.route('**/*', (route) => {
				const url = new URL(route.request().url());
				if (url.origin === origin) return route.continue();
				foreignRequests.push(url.href);
				return route.abort('blockedbyclient');
			});
			page.on('console', (msg) => {
				if (msg.type() === 'error') problems.push(msg.text());
			});
			page.on('pageerror', (error) => problems.push(error.message));

			await use();

			expect(foreignRequests, 'requests to foreign hosts (KONZEPT D27)').toEqual([]);
			expect(problems, 'console errors or CSP violations').toEqual([]);
		},
		{ auto: true }
	]
});

export { expect };
