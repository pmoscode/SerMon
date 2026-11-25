import AxeBuilder from '@axe-core/playwright';
import { installed, mockApi, session } from './api';
import { expect, test } from './fixtures';

const nodeId = '0193f5a0-0000-7000-8000-000000000001';
const now = Date.now();
const node = {
	id: nodeId,
	hostname: 'web-01',
	groupId: 'g1',
	groupName: 'web',
	osId: 'debian',
	osVersion: '13',
	kernelRelease: '6.12.0',
	architecture: 'amd64',
	cpuCount: 4,
	memoryBytes: String(8 * 1024 ** 3),
	agentVersion: '0.1.0',
	enrolledAt: new Date(now - 7 * 86400_000).toISOString(),
	lastSeenAt: new Date(now - 5000).toISOString(),
	online: true,
	figures: {
		cpuBusyRatio: 0.35,
		memoryUsedRatio: 0.62,
		filesystemMaxUsedRatio: 0.91,
		loadPerCpu: 0.4,
		uptimeSeconds: 200000
	}
};

function series(labels: Record<string, string>, base: number) {
	const points = Array.from({ length: 60 }, (_, i) => ({
		time: new Date(now - (60 - i) * 60_000).toISOString(),
		avg: base * (1 + 0.2 * Math.sin(i / 5)),
		min: base,
		max: base
	}));
	return { nodeId, labels, points };
}

const nodeApi = {
	...installed(true),
	'NodeService/GetNode': (body: unknown) =>
		(body as { nodeId: string }).nodeId === nodeId
			? { json: { node } }
			: { status: 404, json: { code: 'not_found', message: 'not found' } },
	'NodeService/ListFilesystems': () => ({
		json: {
			filesystems: [
				{
					mountpoint: '/',
					sizeBytes: '107374182400',
					usedBytes: '97000000000',
					availBytes: '10000000000',
					usedRatio: 0.91,
					inodesTotal: '6000000',
					inodesUsed: '1200000',
					fullAt: new Date(now + 3 * 86400_000).toISOString()
				},
				{
					mountpoint: '/boot',
					sizeBytes: '1000000000',
					usedBytes: '200000000',
					availBytes: '800000000',
					usedRatio: 0.2,
					readonly: true,
					inodesTotal: '65000',
					inodesUsed: '300'
				}
			]
		}
	}),
	// M4-25: the CPU user mode was anomalous for 10 minutes; the node learns (day 3).
	'AnomalyService/GetNodeAnomalies': () => ({
		json: {
			learningSince: new Date(now - 2.5 * 86400_000).toISOString(),
			learningDay: 3,
			series: [
				{
					metric: 'sermon_cpu_utilization_ratio',
					labels: { mode: 'user' },
					maxScore: 9,
					intervals: [
						{
							from: new Date(now - 1800_000).toISOString(),
							to: new Date(now - 1200_000).toISOString()
						}
					]
				}
			]
		}
	}),
	'MetricsService/QueryMetrics': (body: unknown) => {
		const metric = (body as { selector: { metric: string } }).selector.metric;
		if (metric === 'sermon_cpu_utilization_ratio')
			return {
				json: {
					series: [
						series({ mode: 'user' }, 250000),
						series({ mode: 'system' }, 80000),
						series({ mode: 'idle' }, 600000)
					]
				}
			};
		if (metric.startsWith('sermon_load'))
			return { json: { series: [series({ period: '1m' }, 0.4), series({ period: '15m' }, 0.3)] } };
		return { json: { series: [series({}, 1000)] } };
	}
};

test.describe('node detail (mocked API)', () => {
	test('shows facts, file systems and synced charts', async ({ page }) => {
		await mockApi(page, nodeApi);
		await page.goto(`/nodes/${nodeId}`);

		await expect(page.getByRole('heading', { level: 1, name: 'web-01' })).toBeVisible();
		await expect(page.getByText('debian 13 · Kernel 6.12.0 · amd64')).toBeVisible();
		await expect(page.getByRole('figure', { name: 'CPU by mode' })).toBeVisible();
		await expect(page.locator('canvas').first()).toBeVisible();
		await expect(page.getByText(/^user: minimum/)).toBeAttached();
		await expect(page.getByText(/^idle: minimum/)).toHaveCount(0);
		// The anomaly band of the CPU chart and the learning phase (M4-25); the load chart has none.
		await expect(page.getByText('still learning (day 3/7)')).toBeVisible();
		await expect(
			page.getByRole('figure', { name: 'CPU by mode' }).getByText('unusual', { exact: true })
		).toBeVisible();
		await expect(
			page.getByRole('figure', { name: 'Load per CPU' }).getByText('unusual', { exact: true })
		).toHaveCount(0);
		await page.screenshot({ path: 'test-results/node-cpu.png', fullPage: true });
		let results = await new AxeBuilder({ page }).analyze();
		expect(results.violations).toEqual([]);

		await page.getByRole('tab', { name: 'Disks' }).click();
		const fs = page.getByRole('table');
		await expect(fs.getByRole('row')).toHaveCount(3);
		await expect(fs).toContainText('full in 3 days');
		await expect(fs).toContainText('read-only');
		await page.screenshot({ path: 'test-results/node-disk.png', fullPage: true });
		results = await new AxeBuilder({ page }).analyze();
		expect(results.violations).toEqual([]);

		await page.getByRole('tab', { name: 'Network' }).click();
		await page.getByLabel('Show in bit/s').check();
		await expect(page.getByRole('figure', { name: 'Throughput' })).toBeVisible();
	});

	// M2-23: a locked node says so at the top; the identity tab loads on demand and offers the
	// actions to an operator of its group.
	test('shows a lock and the identity tab', async ({ page }) => {
		await mockApi(page, {
			...nodeApi,
			'AuthService/GetSession': () => ({
				json: { session: { ...session, nodeIdentityGroupIds: ['*'] } }
			}),
			'NodeService/GetNode': () => ({
				json: {
					node: {
						...node,
						lockedAt: new Date(Date.now() - 600_000).toISOString(),
						lockReason: 'second session from 192.0.2.66',
						lastIp: '192.0.2.10'
					}
				}
			})
		});
		await page.goto(`/nodes/${nodeId}`);
		await expect(page.getByText('Locked (see Identity)')).toBeVisible();
		await page.getByRole('tab', { name: 'Identity' }).click();
		await expect(page.getByRole('tab', { name: 'Identity' })).toHaveAttribute(
			'aria-selected',
			'true'
		);
		await expect(page.getByRole('tab', { name: 'CPU' })).toHaveAttribute('aria-selected', 'false');
		await expect(page.getByRole('tab', { name: 'Identity' })).toHaveAttribute(
			'data-state',
			'active'
		);
		await expect(page.getByText('second session from 192.0.2.66', { exact: false })).toBeVisible();
		await expect(page.getByRole('button', { name: 'Unlock' })).toBeVisible();
		await page.evaluate(() => Promise.all(document.getAnimations().map((a) => a.finished)));
		await page.screenshot({ path: 'test-results/node-identity.png', fullPage: true });
		const results = await new AxeBuilder({ page }).analyze();
		expect(results.violations).toEqual([]);
	});

	// M2-29: tags under the facts; an admin of the group edits them in place (axe in edit mode).
	test('shows the tags and lets an admin edit them', async ({ page }) => {
		let sent: unknown;
		await mockApi(page, {
			...nodeApi,
			'AuthService/GetSession': () => ({ json: { session: { ...session, tagGroupIds: ['g1'] } } }),
			'NodeService/GetNode': () => ({
				json: { node: { ...node, tags: { env: 'prod', role: 'web' } } }
			}),
			'NodeService/SetNodeTags': (body) => {
				sent = body;
				return { json: {} };
			}
		});
		await page.goto(`/nodes/${nodeId}`);
		await expect(page.getByRole('list', { name: 'Tags' })).toContainText('env=prod');
		await page.getByRole('button', { name: 'Edit tags' }).click();
		await expect(page.getByLabel('New tags')).toHaveValue('env=prod, role=web');
		await page.screenshot({ path: 'test-results/node-tags.png', fullPage: true });
		const results = await new AxeBuilder({ page }).analyze();
		expect(results.violations).toEqual([]);
		await page.getByLabel('New tags').fill('env=prod, role=db');
		await page.getByRole('button', { name: 'Save' }).click();
		await expect.poll(() => sent).toEqual({ nodeId, tags: { env: 'prod', role: 'db' } });
	});

	test.describe('foreign nodes', () => {
		test.use({ expectedHttpErrors: [401, 404] });

		test('say so', async ({ page }) => {
			await mockApi(page, nodeApi);
			await page.goto('/nodes/0193f5a0-0000-7000-8000-0000000000ff');

			await expect(page.getByRole('heading', { name: 'Node not found' })).toBeVisible();
		});
	});
});
