import { create } from '@bufbuild/protobuf';
import { Code, ConnectError } from '@connectrpc/connect';
import { beforeEach, describe, expect, it } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { DashboardSpecSchema, WidgetKind } from '#lib/api/gen/sermon/web/v1/dashboard_pb.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import type { KioskClient } from '#lib/kiosk/api.js';
import { KioskState } from '#lib/kiosk/kiosk.svelte.js';
import KioskView from './KioskView.svelte';

const board = (title: string) =>
	create(DashboardSpecSchema, {
		version: 1,
		title,
		widgets: [
			{
				id: 'alerts',
				kind: WidgetKind.ALERT_LIST,
				title: `${title} alerts`,
				position: { w: 6, h: 3 },
				query: {}
			}
		]
	});

/** Fake KioskServices: per token the dashboard and the widget answer (or a failure). */
function kioskOf(
	answers: Record<string, { dashboard: () => Promise<unknown>; widget?: () => Promise<unknown> }>
) {
	const client = (token: string) =>
		({
			getKioskDashboard: () => answers[token].dashboard(),
			queryKioskWidget: () =>
				(
					answers[token].widget ??
					(() => Promise.resolve({ data: { case: 'alerts', value: { alerts: [] } } }))
				)()
		}) as unknown as KioskClient;
	return new KioskState(Object.keys(answers), { client, now: () => Date.UTC(2026, 10, 26, 9, 30) });
}
const ok = (title: string) => () => Promise.resolve({ spec: board(title), revision: 1n });
const fail = (code: Code) => () => Promise.reject(new ConnectError('x', code));

describe('KioskView', () => {
	beforeEach(() => switchLocale('en'));

	it('says that the address has no token', async () => {
		render(KioskView, { kiosk: kioskOf({}), configured: false });
		await expect
			.poll(() => page.getByRole('alert').element().textContent)
			.toContain('has no kiosk token');
	});

	it('rotates between the dashboards and names the position', async () => {
		const kiosk = kioskOf({ a: { dashboard: ok('Hall') }, b: { dashboard: ok('Lab') } });
		await kiosk.refresh();
		render(KioskView, { kiosk, configured: true });
		await expect.element(page.getByRole('heading', { level: 1, name: 'Hall' })).toBeVisible();
		await expect.element(page.getByText('1 of 2')).toBeVisible();
		await expect.element(page.getByRole('listitem', { name: 'Hall alerts' })).toBeVisible();
		await expect.element(page.getByText('No active alerts.')).toBeVisible();
		kiosk.rotate();
		await expect.element(page.getByRole('heading', { level: 1, name: 'Lab' })).toBeVisible();
		await expect.element(page.getByText('2 of 2')).toBeVisible();
		expect(page.getByRole('button').elements()).toHaveLength(0);
	});

	it('shows nothing once the token stops working', async () => {
		const answers = { a: { dashboard: ok('Hall') } };
		const kiosk = kioskOf(answers);
		await kiosk.refresh();
		render(KioskView, { kiosk, configured: true });
		await expect.element(page.getByRole('heading', { name: 'Hall' })).toBeVisible();
		answers.a.dashboard = fail(Code.Unauthenticated);
		await kiosk.refresh();
		await expect
			.poll(() => page.getByRole('alert').element().textContent)
			.toContain('invalid, expired or revoked');
		expect(page.getByRole('heading', { name: 'Hall' }).elements()).toHaveLength(0);
		expect(page.getByRole('list').elements()).toHaveLength(0);
	});

	it('keeps the data through an outage and says since when it is not current', async () => {
		const answers = { a: { dashboard: ok('Hall') } };
		const kiosk = kioskOf(answers);
		await kiosk.refresh();
		render(KioskView, { kiosk, configured: true });
		answers.a.dashboard = fail(Code.Unavailable);
		await kiosk.refresh();
		await expect
			.poll(() => page.getByText(/Not current since/).element().textContent)
			.toContain('reconnecting');
		await expect.element(page.getByRole('heading', { name: 'Hall' })).toBeVisible();
		answers.a.dashboard = ok('Hall');
		await kiosk.refresh();
		await expect.poll(() => page.getByText(/Not current since/).elements().length).toBe(0);
	});

	it('marks the dashboard stale while a widget fails', async () => {
		const kiosk = kioskOf({ a: { dashboard: ok('Hall'), widget: fail(Code.Unavailable) } });
		await kiosk.refresh();
		render(KioskView, { kiosk, configured: true });
		await expect.element(page.getByText('The data could not be loaded.')).toBeVisible();
		await expect.element(page.getByText(/Not current since/)).toBeVisible();
	});

	it('says that the server can’t be reached before the first data', async () => {
		const kiosk = kioskOf({ a: { dashboard: fail(Code.Unavailable) } });
		await kiosk.refresh();
		render(KioskView, { kiosk, configured: true });
		await expect
			.poll(() => page.getByRole('alert').element().textContent)
			.toContain('can’t be reached');
	});
});
