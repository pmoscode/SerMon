import AxeBuilder from '@axe-core/playwright';
import { installed, mockApi, type Handler } from './api';
import { expect, test } from './fixtures';

const web01 = '0193f5a0-0000-7000-8000-000000000001';
const db01 = '0193f5a0-0000-7000-8000-000000000002';
const now = Date.now();
const hostile = '<img src=x onerror=alert(1)>\u202Elacol';

function updatesApi(): Record<string, Handler> {
	return {
		...installed(true),
		'UpdateService/ListFleetUpdates': () => ({
			json: {
				nodes: [
					{
						nodeId: web01,
						hostname: 'web-01',
						status: 'UPDATE_STATUS_SECURITY',
						availability: 'UPDATE_AVAILABILITY_LISTED',
						totalCount: 4,
						securityCount: 2,
						rebootRequired: true,
						collectedAt: new Date(now - 3600_000).toISOString(),
						oldestSecuritySince: new Date(now - 3 * 86400_000).toISOString()
					},
					{
						nodeId: db01,
						hostname: 'db-01',
						status: 'UPDATE_STATUS_UNAVAILABLE',
						availability: 'UPDATE_AVAILABILITY_FAILED',
						reason: hostile
					}
				],
				withSecurity: 1,
				unavailable: 1,
				rebootRequired: 1
			}
		}),
		'UpdateService/ListRollouts': () => ({
			json: {
				rollouts: [
					{
						rolloutId: 'r1',
						groupName: 'web',
						state: 'ROLLOUT_STATE_STOPPED',
						reason: 'web-01: update: failed with exit code 100',
						windowStart: new Date(now - 7200_000).toISOString(),
						nodes: [
							{
								nodeId: web01,
								hostname: 'web-01',
								state: 'ROLLOUT_NODE_STATE_FAILED',
								reason: hostile
							},
							{
								nodeId: db01,
								hostname: 'db-01',
								state: 'ROLLOUT_NODE_STATE_SKIPPED',
								reason: 'the rollout stopped'
							}
						]
					}
				]
			}
		}),
		'UpdatePolicyService/ListUpdatePolicies': () => ({
			json: {
				policies: [
					{
						groupId: 'g1',
						groupName: 'web',
						spec: { mode: 'UPDATE_MODE_SECURITY_AUTO', maxParallel: 1, healthCheckMinutes: 10 },
						nextWindow: new Date(now + 86400_000).toISOString()
					}
				]
			}
		})
	};
}

test('the updates page shows the nodes by status, the rollouts and the policies', async ({
	page
}) => {
	await mockApi(page, updatesApi());
	await page.goto('/updates');
	await expect(page.getByRole('heading', { level: 1, name: 'Updates' })).toBeVisible();
	const nodes = page.getByRole('table', { name: 'Nodes' });
	await expect(nodes.getByRole('row', { name: /web-01/ })).toContainText('Security updates');
	await expect(nodes.getByRole('row', { name: /web-01/ })).toContainText('3 days ago');
	// Text from the hosts stays text, bidi overrides visible.
	await expect(page.getByText('<img src=x onerror=alert(1)>⟨U+202E⟩lacol').first()).toBeVisible();
	expect(await page.locator('img[src="x"]').count()).toBe(0);
	await page.getByRole('button', { name: /Reboot needed/ }).click();
	await expect(nodes.getByRole('row')).toHaveCount(2);
	await expect(page.getByText(/web-01: failed/)).toBeVisible();
	await expect(page.getByText('security updates automatically')).toBeVisible();
	const results = await new AxeBuilder({ page }).analyze();
	expect(results.violations).toEqual([]);
});

test('the updates page loads less than 200 KiB of JavaScript', async ({ page }) => {
	await mockApi(page, updatesApi());
	const sizes = new Map<string, number>();
	const { gzipSync } = await import('node:zlib');
	page.on('response', async (response) => {
		if (response.url().endsWith('.js'))
			sizes.set(response.url(), gzipSync(await response.body()).length);
	});
	await page.goto('/updates');
	await page.waitForLoadState('networkidle');
	const size = [...sizes.values()].reduce((a, b) => a + b, 0);
	console.log(`updates: ${(size / 1024).toFixed(1)} KiB gzip`);
	expect(size).toBeLessThan(190 * 1024);
});
