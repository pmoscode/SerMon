import AxeBuilder from '@axe-core/playwright';
import { expect, test } from '../e2e/fixtures';
import { PASSWORD, admin, ageStepUp, call, login } from './stack';

/**
 * The kiosk of the M5 demo (IMPLEMENTIERUNGSPLAN M5-32), after modules.demo.ts: an admin imports
 * a dashboard of web-01's nginx, shares it and creates a kiosk token with the step-up. A browser
 * without a session shows the dashboard with live data from the token alone – no cookie, the token
 * only in the fragment. Once revoked, the running display shows nothing more.
 */

// The step-up asks once with FAILED_PRECONDITION (400); the revoked token's requests answer 401.
test.use({ expectedHttpErrors: [400, 401] });

const title = 'Web shop wall';

test('a dashboard runs on a kiosk display with its token alone and shows nothing once the token is revoked', async ({
	page,
	browser,
	baseURL
}) => {
	test.setTimeout(10 * 60_000);
	const base = baseURL ?? '';
	await login(page, admin.email);
	const fleet = await call<{ nodes?: { nodeId: string; hostname: string; groupId: string }[] }>(
		page.request,
		base,
		'FleetService/GetFleetSummary',
		{}
	);
	const web01 = fleet.nodes!.find((n) => n.hostname === 'web-01')!;

	// The dashboard as its JSON export would be: nginx's requests and connections on web-01, the
	// alerts of its group (modules.demo.ts gave it the group "web").
	const spec = {
		version: 1,
		title,
		timeRange: 'TIME_PRESET_15M',
		widgets: [
			{
				id: 'requests',
				kind: 'WIDGET_KIND_TIMESERIES',
				title: 'nginx requests',
				position: { x: 0, y: 0, w: 8, h: 4 },
				query: { metric: 'sermon_nginx_requests_per_second', target: { nodeIds: [web01.nodeId] } }
			},
			{
				id: 'connections',
				kind: 'WIDGET_KIND_STAT',
				title: 'nginx connections',
				position: { x: 8, y: 0, w: 4, h: 4 },
				query: { metric: 'sermon_nginx_connections_active', target: { nodeIds: [web01.nodeId] } }
			},
			{
				id: 'alerts',
				kind: 'WIDGET_KIND_ALERT_LIST',
				title: 'Alerts of the web servers',
				position: { x: 0, y: 4, w: 12, h: 3 },
				query: { target: { groupId: web01.groupId } }
			}
		]
	};
	await page.goto('/dashboards');
	await page.getByLabel('Import JSON').setInputFiles({
		name: 'web-shop-wall.json',
		mimeType: 'application/json',
		buffer: Buffer.from(JSON.stringify(spec))
	});
	await expect(page).toHaveURL(/\/dashboards\/[0-9a-f-]{36}$/);
	await expect(page.getByRole('heading', { level: 1, name: title })).toBeVisible();
	await page.getByRole('button', { name: 'Share in the organization' }).click();
	await expect(page.getByText('Shared', { exact: true })).toBeVisible();

	// A kiosk token for the display in the hall, with the step-up.
	ageStepUp(admin.email);
	await page.getByRole('button', { name: 'Kiosk displays' }).click();
	await page.getByLabel('Where the display hangs').fill('Hall');
	await page.getByRole('button', { name: 'Create token' }).click();
	const stepUp = page.getByRole('dialog').filter({ hasText: 'Confirm your identity' });
	await stepUp.getByLabel('Password').fill(PASSWORD);
	await stepUp.getByRole('button', { name: 'Confirm' }).click();
	const field = page.getByLabel('Address of the display');
	await expect(field).toHaveValue(/\/kiosk#t=smk_[\w-]{43}$/);
	const address = await field.inputValue();
	const token = new URL(address).hash.replace(/^#t=/, '');
	await page.screenshot({ path: 'test-results/demo-kiosk-token.png', fullPage: true });

	// The display: another browser without a session. It sends the token as a header, never a
	// cookie, and no address with the token reaches the server.
	const display = await browser.newContext({ ignoreHTTPSErrors: true });
	const kiosk = await display.newPage();
	const requests: { url: string; authorization?: string; cookie?: string }[] = [];
	kiosk.on('request', (r) => {
		const headers = r.headers();
		requests.push({ url: r.url(), authorization: headers.authorization, cookie: headers.cookie });
	});
	await kiosk.goto(address);
	await expect(kiosk.getByRole('heading', { level: 1, name: title })).toBeVisible();
	const grid = kiosk.getByRole('list', { name: `Widgets of ${title}` });
	const requestsWidget = grid.getByRole('listitem', { name: 'nginx requests' });
	const connections = grid.getByRole('listitem', { name: 'nginx connections' });
	await expect(requestsWidget.locator('canvas').first()).toBeVisible({ timeout: 30_000 });
	await expect(connections).toContainText(/\d/, { timeout: 30_000 });
	await expect(grid.getByText('No data in this range, or no access to the nodes.')).toHaveCount(0);
	await expect(grid.getByRole('listitem', { name: 'Alerts of the web servers' })).toBeVisible();
	expect(await kiosk.getByRole('button').count()).toBe(0);
	expect((await new AxeBuilder({ page: kiosk }).analyze()).violations).toEqual([]);
	await kiosk.screenshot({ path: 'test-results/demo-kiosk.png', fullPage: true });
	const api = requests.filter((r) => r.url.includes('/sermon.web.v1.'));
	expect(api.length).toBeGreaterThan(0);
	for (const r of api) {
		expect(r.url, r.url).toContain('/sermon.web.v1.KioskService/');
		expect(r.authorization).toBe(`Kiosk ${token}`);
		expect(r.cookie).toBeUndefined();
	}
	expect(requests.some((r) => r.url.includes(token))).toBe(false);
	// The token opens nothing but its dashboard: the fleet answers it like a stranger.
	const fleetWithToken = await kiosk.evaluate(async (t) => {
		const res = await fetch('/sermon.web.v1.FleetService/GetFleetSummary', {
			method: 'POST',
			headers: { 'Content-Type': 'application/json', Authorization: `Kiosk ${t}` },
			body: '{}',
			credentials: 'omit'
		});
		return res.status;
	}, token);
	expect(fleetWithToken).toBe(401);

	// Revoked in the admin's browser: the running display notices at its next refresh (30 s)
	// and shows nothing of the dashboard any more – after a reload neither.
	await page.getByRole('button', { name: 'Revoke Hall' }).click();
	await expect(page.getByText(/·\s*revoked/)).toBeVisible();
	const revoked = Date.now();
	await expect(kiosk.getByRole('alert')).toContainText('invalid, expired or revoked', {
		timeout: 60_000
	});
	console.log(
		`the display went blank ${Math.round((Date.now() - revoked) / 1000)} s after the revocation`
	);
	await expect(kiosk.getByRole('heading', { name: title })).toHaveCount(0);
	await expect(kiosk.getByRole('list', { name: `Widgets of ${title}` })).toHaveCount(0);
	await kiosk.reload();
	await expect(kiosk.getByRole('alert')).toContainText('invalid, expired or revoked');
	await expect(kiosk.getByText('nginx connections')).toHaveCount(0);
	await kiosk.screenshot({ path: 'test-results/demo-kiosk-revoked.png', fullPage: true });
	await display.close();
});
