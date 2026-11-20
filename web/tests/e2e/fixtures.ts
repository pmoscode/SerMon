import { test as base, expect } from '@playwright/test';

/**
 * Every E2E test runs with a guard that enforces KONZEPT D27 and the strict CSP:
 * - requests to any host other than sermon-server are blocked and fail the test,
 * - console errors (including CSP violations) and uncaught exceptions fail the test. The
 *   exception is the browser's note on API answers a test expects: 401 (nobody logged in, wrong
 *   password) always, others like 404 for a foreign node when the test declares them.
 */
const httpError = /^Failed to load resource: the server responded with a status of (\d{3}) /;

export const test = base.extend<{ guard: void; expectedHttpErrors: number[] }>({
	expectedHttpErrors: [[401], { option: true }],
	guard: [
		async ({ page, baseURL, expectedHttpErrors }, use) => {
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
				const status = httpError.exec(msg.text())?.[1];
				if (msg.type() === 'error' && !(status && expectedHttpErrors.includes(Number(status)))) {
					problems.push(msg.text());
				}
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
