import AxeBuilder from '@axe-core/playwright';
import { installed, mockApi, session, type Handler } from './api';
import { expect, test } from './fixtures';

const token = `smk_${'k'.repeat(43)}`;
const boardId = '0193f5a0-0000-7000-8000-0000000000d1';
const hostile = '<img src=x onerror=alert(1)>';

function board(title: string) {
	return {
		dashboardId: boardId,
		revision: '1',
		spec: {
			version: 1,
			title,
			timeRange: 'TIME_PRESET_24H',
			widgets: [
				{
					id: 'alerts',
					kind: 'WIDGET_KIND_ALERT_LIST',
					title: 'Alerts',
					position: { w: 6, h: 3 },
					query: {}
				},
				{
					id: 'cpu',
					kind: 'WIDGET_KIND_STAT',
					title: 'CPU',
					position: { x: 6, w: 3, h: 3 },
					query: { metric: 'sermon_cpu_utilization_ratio', target: { variable: 'host' } }
				}
			]
		}
	};
}

test.describe('kiosk display (mocked API)', () => {
	// 401: the revoked token.
	test.use({ expectedHttpErrors: [401] });

	test('shows the dashboard of its token without a session and nothing once revoked', async ({
		page
	}) => {
		let revoked = false;
		const headers: { authorization?: string; cookie?: string }[] = [];
		const answer =
			(json: unknown): Handler =>
			() =>
				revoked
					? { status: 401, json: { code: 'unauthenticated', message: 'invalid kiosk token' } }
					: { json };
		await page
			.context()
			.addCookies([{ name: 'other', value: 'x', domain: '127.0.0.1', path: '/' }]);
		page.on('request', (r) => {
			if (r.url().includes('KioskService')) headers.push(r.headers());
		});
		await mockApi(page, {
			'KioskService/GetKioskDashboard': answer(board('Hall')),
			'KioskService/QueryKioskWidget': (body) => {
				if (revoked) return { status: 401, json: { code: 'unauthenticated', message: 'x' } };
				const id = (body as { widgetId: string }).widgetId;
				return id === 'alerts'
					? {
							json: {
								alerts: {
									alerts: [
										{
											key: { ruleId: 'system.cpu.usage_high', nodeId: 'n1' },
											state: 'ALERT_STATE_CRITICAL',
											hostname: hostile,
											ruleName: { en: 'CPU usage high' }
										}
									]
								}
							}
						}
					: {
							json: {
								metrics: {
									unit: 'ratio_ppm',
									hostnames: { n1: 'web-01' },
									series: [
										{
											nodeId: 'n1',
											points: [
												{ time: new Date().toISOString(), avg: 420_000, min: 0, max: 500_000 }
											]
										}
									]
								}
							}
						};
			}
		});
		await page.goto(`/kiosk#t=${token}`);
		await expect(page.getByRole('heading', { level: 1, name: 'Hall' })).toBeVisible();
		const grid = page.getByRole('list', { name: 'Widgets of Hall' });
		await expect(grid.getByText('CPU usage high')).toBeVisible();
		await expect(grid.getByText('42%')).toBeVisible();
		await expect(grid.getByText(hostile)).toBeVisible();
		expect(await page.locator('img[src="x"]').count()).toBe(0);
		expect(await page.getByRole('button').count()).toBe(0);
		expect(headers.length).toBeGreaterThan(0);
		for (const h of headers) {
			expect(h.authorization).toBe(`Kiosk ${token}`);
			expect(h.cookie).toBeUndefined();
		}
		// The token stays in the fragment: the server never sees it in an address.
		expect(page.url()).toContain(`#t=${token}`);
		expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);

		revoked = true;
		await page.reload();
		await expect(page.getByRole('alert')).toContainText('invalid, expired or revoked');
		await expect(page.getByRole('heading', { name: 'Hall' })).toHaveCount(0);
		await expect(page.getByText('CPU usage high')).toHaveCount(0);
	});

	test('an address without a token says what to do', async ({ page }) => {
		await mockApi(page, {});
		await page.goto('/kiosk');
		await expect(page.getByRole('alert')).toContainText('has no kiosk token');
	});
});

test.describe('kiosk tokens of a dashboard (mocked API)', () => {
	test('an admin creates a token and gets the address of the display once', async ({ page }) => {
		const seen: unknown[] = [];
		let tokens: unknown[] = [];
		await mockApi(page, {
			...installed(true),
			'AuthService/GetSession': () => ({ json: { session: { ...session, orgAdmin: true } } }),
			'DashboardService/GetDashboard': () => ({
				json: {
					dashboard: {
						id: boardId,
						ownerId: session.userId,
						revision: '2',
						canEdit: true,
						shared: true,
						spec: board('Hall').spec
					}
				}
			}),
			'MetricsService/QueryMetrics': () => ({ json: {} }),
			'MetricsService/ListMetrics': () => ({ json: {} }),
			'AlertService/ListAlerts': () => ({ json: {} }),
			'KioskTokenService/ListKioskTokens': () => ({ json: { tokens } }),
			'KioskTokenService/CreateKioskToken': (body) => {
				seen.push(body);
				tokens = [
					{
						id: '0193f5a0-0000-7000-8000-0000000000e1',
						dashboardId: boardId,
						name: 'Hall',
						createdByName: 'Mia Muster',
						createTime: new Date().toISOString(),
						expireTime: new Date(Date.now() + 90 * 86_400_000).toISOString()
					}
				];
				return { json: { token: tokens[0], secret: token } };
			},
			'KioskTokenService/RevokeKioskToken': () => ({ json: {} })
		});
		await page.goto(`/dashboards/${boardId}`);
		await page.getByRole('button', { name: 'Kiosk displays' }).click();
		await expect(page.getByText('No kiosk tokens yet.')).toBeVisible();
		await page.getByLabel('Where the display hangs').fill('Hall');
		await page.getByRole('button', { name: 'Create token' }).click();
		await expect(page.getByLabel('Address of the display')).toHaveValue(
			new RegExp(`/kiosk#t=${token}$`)
		);
		expect(seen[0]).toMatchObject({ dashboardId: boardId, name: 'Hall', validFor: '7776000s' });
		await expect(page.getByRole('button', { name: 'Revoke Hall' })).toBeVisible();
		expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
	});
});
