import AxeBuilder from '@axe-core/playwright';
import { installed, mockApi, type Handler } from './api';
import { expect, test } from './fixtures';

const web01 = '0193f5a0-0000-7000-8000-000000000001';
const db01 = '0193f5a0-0000-7000-8000-000000000002';
const now = Date.now();
const hostile = '<img src=x onerror=alert(1)><script>alert(2)</script>\u202Etoor';
const csv = `occurred_at,user\r\n2026-11-24T06:00:00Z,"'=HYPERLINK(""http://evil.example"")"\r\n`;

function event(id: number, extra: Record<string, unknown> = {}) {
	return {
		id: String(id),
		nodeId: web01,
		occurredAt: new Date(now - id * 60_000).toISOString(),
		kind: 'ssh.login.failed',
		user: 'root',
		remoteIp: '192.0.2.66',
		remotePort: 50022,
		details: { method: 'password' },
		...extra
	};
}

function securityApi(lists: unknown[] = [], exports: unknown[] = []): Record<string, Handler> {
	return {
		...installed(true),
		'FleetService/GetFleetSummary': () => ({
			json: {
				nodes: [
					{ nodeId: web01, hostname: 'web-01', groupId: 'g1', groupName: 'web', online: true },
					{ nodeId: db01, hostname: 'db-01', groupId: 'g1', groupName: 'web', online: true }
				]
			}
		}),
		'SecurityEventService/GetSecurityOverview': () => ({
			json: {
				step: '900s',
				failedLogins: [
					{ nodeId: web01, points: [{ at: new Date(now - 3600_000).toISOString(), count: '40' }] },
					{ nodeId: db01, points: [{ at: new Date(now - 1800_000).toISOString(), count: '2' }] }
				],
				topIps: [
					{
						value: '192.0.2.66',
						count: '40',
						nodes: 2,
						lastAt: new Date(now).toISOString(),
						unverified: '3'
					}
				],
				topUsers: [{ value: hostile, count: '30', nodes: 1, lastAt: new Date(now).toISOString() }],
				totals: {
					failedLogins: '42',
					logins: '7',
					newSourceLogins: '2',
					rootLogins: '1',
					sudoFailures: '3'
				}
			}
		}),
		'SecurityEventService/ListSecurityEvents': (body) => {
			lists.push(body);
			return {
				json: {
					events: [
						event(1, { user: hostile, unverified: true }),
						event(2, { kind: 'ssh.login.success', user: 'alice', newSource: true })
					]
				}
			};
		},
		'SecurityEventService/ExportSecurityEvents': (body) => {
			exports.push(body);
			return {
				json: {
					fileName: 'sermon-security-events.csv',
					content: Buffer.from(csv).toString('base64'),
					rows: 1
				}
			};
		}
	};
}

test('the security dashboard shows hostile names as text and passes axe', async ({ page }) => {
	await mockApi(page, securityApi());
	await page.goto('/security');
	await expect(page.getByRole('heading', { name: 'Security', level: 1 })).toBeVisible();
	const table = page.getByRole('table');
	await expect(table.getByText('toor', { exact: false })).toHaveText(
		'<img src=x onerror=alert(1)><script>alert(2)</script>⟨U+202E⟩toor'
	);
	await expect(table.getByText('unverified', { exact: true })).toBeVisible();
	await expect(table.getByText('new', { exact: true })).toBeVisible();
	await expect(page.getByRole('figure')).toContainText('web-01');
	expect(await page.locator('img[src="x"]').count()).toBe(0);

	const results = await new AxeBuilder({ page }).analyze();
	expect(results.violations).toEqual([]);
});

test('the filter lives in the URL', async ({ page }) => {
	const lists: unknown[] = [];
	await mockApi(page, securityApi(lists));
	await page.goto(`/security?range=7d&node=${db01}&kind=ssh.login.success&user=root&new=1`);
	await expect(page.getByLabel('Node', { exact: true })).toHaveValue(db01);
	await expect(page.getByLabel('Kind')).toHaveValue('ssh.login.success');
	await expect(page.getByLabel('User', { exact: true })).toHaveValue('root');
	await expect(page.getByLabel('Only logins from new sources')).toBeChecked();
	await expect.poll(() => lists.length).toBeGreaterThan(0);
	expect(lists.at(-1)).toMatchObject({
		nodeIds: [db01],
		kinds: ['ssh.login.success'],
		user: 'root',
		newSourcesOnly: true
	});

	await page.getByRole('button', { name: 'Show events of 192.0.2.66' }).click();
	await expect(page).toHaveURL(/net=192.0.2.66/);
	await expect(page).toHaveURL(/kind=ssh.login.failed&kind=ssh.login.invalid_user/);
	await expect(page.getByLabel('Kind')).toHaveValue('failed');
	await expect.poll(() => (lists.at(-1) as { network?: string }).network).toBe('192.0.2.66');

	await page.getByRole('button', { name: '30 days' }).click();
	await expect(page).toHaveURL(/range=30d/);
	await page.goBack();
	await expect(page).not.toHaveURL(/range=30d/);
	await expect(page.getByLabel('IP or network')).toHaveValue('192.0.2.66');
});

test('the export downloads the CSV of the filter', async ({ page }) => {
	const exports: unknown[] = [];
	await mockApi(page, securityApi([], exports));
	await page.goto('/security?user=root');
	await expect(page.getByRole('table')).toBeVisible();
	const download = page.waitForEvent('download');
	await page.getByRole('button', { name: 'Export as CSV' }).click();
	const file = await download;
	expect(file.suggestedFilename()).toBe('sermon-security-events.csv');
	const stream = await file.createReadStream();
	const chunks: Buffer[] = [];
	for await (const chunk of stream) chunks.push(chunk as Buffer);
	expect(Buffer.concat(chunks).toString()).toBe(csv);
	await expect(page.getByText('Saved 1 events as CSV.')).toBeVisible();
	expect(exports[0]).toMatchObject({ filter: { user: 'root' } });
});

test('the node page has a security tab that leads to the dashboard', async ({ page }) => {
	await mockApi(page, {
		...securityApi(),
		'NodeService/GetNode': () => ({
			json: { node: { id: web01, hostname: 'web-01', groupName: 'web', online: true } }
		}),
		'NodeService/ListFilesystems': () => ({ json: {} }),
		'MetricsService/QueryMetrics': () => ({ json: {} })
	});
	await page.goto(`/nodes/${web01}`);
	await page.getByRole('tab', { name: 'Security' }).click();
	await expect(page.getByRole('table').getByText('alice')).toBeVisible();
	await expect(page.getByLabel('Node', { exact: true })).toHaveCount(0);
	const results = await new AxeBuilder({ page }).analyze();
	expect(results.violations).toEqual([]);
	await page.getByRole('link', { name: 'Open in the security dashboard' }).click();
	await expect(page).toHaveURL(new RegExp(`/security\\?node=${web01}`));
	await expect(page.getByLabel('Node', { exact: true })).toHaveValue(web01);
});

test('the menu leads to the dashboard and the account menu to the account security', async ({
	page
}) => {
	await mockApi(page, { ...securityApi(), 'MetricsService/QueryMetrics': () => ({ json: {} }) });
	await page.goto('/fleet');
	await page.getByRole('navigation').getByRole('link', { name: 'Security' }).click();
	await expect(page).toHaveURL(/\/security$/);
	await page
		.getByRole('button', { name: /Account|Mia/ })
		.first()
		.click();
	await page.getByRole('link', { name: 'Account security' }).click();
	await expect(page).toHaveURL(/\/settings\/security$/);
});
