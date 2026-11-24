import { create } from '@bufbuild/protobuf';
import { timestampFromMs } from '@bufbuild/protobuf/wkt';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { Code } from '#lib/api/client.js';
import { alertingApi } from '#lib/api/alerting.js';
import {
	AlertHistoryKind,
	AlertSchema,
	AlertState,
	ListAlertsResponseSchema
} from '#lib/api/gen/sermon/web/v1/alert_pb.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { m } from '#lib/paraglide/messages.js';
import { stepUpState } from '#lib/stores/stepup.svelte.js';
import { rpcError, stepUpError } from '#lib/test/fakes.js';
import AlertsView from './AlertsView.svelte';

const now = Date.now();
const hostile = '<img src=x onerror=alert(1)>';

const brute = create(AlertSchema, {
	key: {
		ruleId: 'security.ssh.bruteforce',
		nodeId: '0193f5a0-0000-7000-8000-000000000001',
		dimension: 'ip=192.0.2.7'
	},
	state: AlertState.CRITICAL,
	ruleName: { de: 'SSH-Brute-Force', en: 'SSH brute force' },
	dimensionLabels: { ip: '192.0.2.7' },
	hostname: 'web-01',
	groupId: 'g1',
	groupName: 'web',
	since: timestampFromMs(now - 120_000),
	fireTime: timestampFromMs(now - 300_000),
	labels: { category: 'security' },
	value: 60,
	unit: 'count',
	fields: { user: hostile },
	muted: true,
	canAcknowledge: true
});
const disk = create(AlertSchema, {
	key: {
		ruleId: 'system.filesystem.full_soon',
		nodeId: '0193f5a0-0000-7000-8000-000000000002',
		dimension: 'mountpoint=/var'
	},
	state: AlertState.PENDING,
	ruleName: { de: 'Dateisystem bald voll', en: 'File system full soon' },
	dimensionLabels: { mountpoint: '/var' },
	hostname: 'db-01',
	groupId: 'g2',
	groupName: 'db',
	since: timestampFromMs(now - 60_000),
	value: 5.25,
	unit: 'hours'
});

function listing(...alerts: (typeof brute)[]) {
	return create(ListAlertsResponseSchema, { alerts });
}

describe('AlertsView', () => {
	beforeEach(() => switchLocale('en'));
	afterEach(() => {
		vi.restoreAllMocks();
		if (stepUpState.open) stepUpState.finish(false);
	});

	it('leads to the charts of the node around the firing, in the area of the rule (M3-19)', async () => {
		vi.spyOn(alertingApi.alerts, 'listAlerts').mockResolvedValue(listing(brute, disk));
		render(AlertsView);
		const links = page.getByRole('link', { name: 'Charts around the time' });
		await expect.element(links.first()).toBeVisible();
		const href = new URL(links.first().element().getAttribute('href')!, 'http://x');
		expect(href.pathname).toBe('/nodes/0193f5a0-0000-7000-8000-000000000001');
		expect(Date.parse(href.searchParams.get('from')!)).toBe(now - 300_000 - 3600_000);
		expect(href.searchParams.get('tab')).toBe('security');
		// The pending alert by its start, without an area for its rule.
		const pending = new URL(links.nth(1).element().getAttribute('href')!, 'http://x');
		expect(Date.parse(pending.searchParams.get('from')!)).toBe(now - 60_000 - 3600_000);
		expect(pending.searchParams.get('tab')).toBeNull();
	});

	it('shows the alerts with their state, values and host data as text', async () => {
		vi.spyOn(alertingApi.alerts, 'listAlerts').mockResolvedValue(listing(brute, disk));
		render(AlertsView);

		const cards = page.getByRole('article');
		await expect.element(cards.first()).toMatchTextContent('SSH brute force');
		expect(cards.elements()).toHaveLength(2);
		await expect.element(cards.first()).toMatchTextContent('Critical');
		await expect.element(cards.first()).toMatchTextContent('Muted');
		await expect.element(cards.first()).toMatchTextContent('ip: 192.0.2.7');
		// The hostile user name stays text.
		await expect.element(cards.first()).toMatchTextContent(hostile);
		expect(document.querySelector('img')).toBeNull();
		await expect.element(cards.nth(1)).toMatchTextContent('Pending');
		await expect.element(cards.nth(1)).toMatchTextContent('5.25 h'); // the matcher normalizes the no-break space;
		// Only what the caller may acknowledge has the button.
		expect(page.getByRole('button', { name: /Acknowledge/ }).elements()).toHaveLength(1);
		await expect.element(page.getByText('1 firing, 1 of them critical')).toBeVisible();
	});

	// ADR 0025: events from classic log files; anyone on the host could have written them.
	it('marks alerts from unverified log files with icon, text and help', async () => {
		const forged = create(AlertSchema, {
			...brute,
			key: { ...brute.key!, dimension: 'ip=192.0.2.8' },
			dimensionLabels: { ip: '192.0.2.8' },
			fields: { user: 'root', source: 'unverified' }
		});
		vi.spyOn(alertingApi.alerts, 'listAlerts').mockResolvedValue(listing(forged, brute));
		render(AlertsView);

		const note = page.getByRole('note');
		await expect
			.element(note)
			.toMatchTextContent('Source not verified: a log file any local user can write to.');
		expect(note.elements()).toHaveLength(1);
		await expect
			.element(page.getByRole('button', { name: 'Help: Unverified source' }))
			.toBeVisible();
		// Shown as the note, not again as a field.
		const card = page.getByRole('article').filter({ hasText: '192.0.2.8' });
		expect(card.element().textContent).not.toContain('unverified');
	});

	it('filters by state, group and search', async () => {
		vi.spyOn(alertingApi.alerts, 'listAlerts').mockResolvedValue(listing(brute, disk));
		render(AlertsView);

		await page.getByLabelText('State').selectOptions('pending');
		await expect.element(page.getByRole('article')).toMatchTextContent('db-01');
		await page.getByLabelText('State').selectOptions('');
		await page.getByLabelText('Group').selectOptions('web');
		await expect.element(page.getByRole('article')).toMatchTextContent('web-01');
		await page.getByLabelText('Search').fill('nothing like this');
		await expect.element(page.getByText(m.alerts_no_match())).toBeVisible();
		await page.getByRole('button', { name: 'Reset filter' }).click();
		expect(page.getByRole('article').elements()).toHaveLength(2);
	});

	it('acknowledges after the step-up and reloads', async () => {
		const list = vi
			.spyOn(alertingApi.alerts, 'listAlerts')
			.mockResolvedValueOnce(listing(brute))
			.mockResolvedValue(
				listing(
					create(AlertSchema, {
						...brute,
						canAcknowledge: false,
						acknowledgedBy: 'Mia',
						acknowledgeTime: timestampFromMs(now)
					})
				)
			);
		const ack = vi
			.spyOn(alertingApi.alerts, 'acknowledgeAlert')
			.mockRejectedValueOnce(stepUpError())
			.mockResolvedValueOnce({} as never);
		render(AlertsView);

		await page.getByRole('button', { name: 'Acknowledge SSH brute force on web-01' }).click();
		await vi.waitFor(() => expect(stepUpState.open).toBe(true));
		stepUpState.finish(true);

		await expect.element(page.getByText(/Acknowledged by Mia/)).toBeVisible();
		expect(ack).toHaveBeenCalledTimes(2);
		expect(ack.mock.calls[1][0]).toEqual({ key: brute.key });
		expect(list).toHaveBeenCalledTimes(2);
		expect(page.getByRole('button', { name: /Acknowledge/ }).elements()).toHaveLength(0);
	});

	it('explains an alert that stopped firing meanwhile', async () => {
		vi.spyOn(alertingApi.alerts, 'listAlerts').mockResolvedValue(listing(brute));
		vi.spyOn(alertingApi.alerts, 'acknowledgeAlert').mockRejectedValue(
			rpcError(Code.FailedPrecondition)
		);
		render(AlertsView);

		await page.getByRole('button', { name: /Acknowledge/ }).click();
		await expect.element(page.getByRole('alert')).toHaveTextContent(m.alert_error_not_firing());
	});

	it('opens the history on demand', async () => {
		vi.spyOn(alertingApi.alerts, 'listAlerts').mockResolvedValue(listing(brute));
		const history = vi.spyOn(alertingApi.alerts, 'getAlertHistory').mockResolvedValue({
			entries: [
				{ kind: AlertHistoryKind.ACKNOWLEDGED, by: 'Mia', time: timestampFromMs(now), fields: {} },
				{
					kind: AlertHistoryKind.STATE_CHANGED,
					from: AlertState.OK,
					to: AlertState.CRITICAL,
					value: 60,
					time: timestampFromMs(now - 60_000),
					fields: { user: 'root' }
				}
			]
		} as never);
		render(AlertsView);

		const toggle = page.getByRole('button', { name: 'History of SSH brute force on web-01' });
		await expect.element(toggle).toHaveAttribute('aria-expanded', 'false');
		expect(history).not.toHaveBeenCalled();
		await toggle.click();
		await expect.element(toggle).toHaveAttribute('aria-expanded', 'true');
		const region = page.getByRole('region', { name: 'History of SSH brute force on web-01' });
		await expect.element(region).toMatchTextContent('Acknowledged by Mia');
		await expect.element(region).toMatchTextContent('OK → Critical');
		await expect.element(region).toMatchTextContent('User: root');
	});

	it('has loading, error with retry and empty states', async () => {
		let release: (v: ReturnType<typeof listing>) => void = () => {};
		const list = vi
			.spyOn(alertingApi.alerts, 'listAlerts')
			.mockImplementationOnce(() => new Promise((resolve) => (release = resolve)) as never)
			.mockRejectedValueOnce(rpcError(Code.Unavailable))
			.mockResolvedValueOnce(listing());
		const view = await render(AlertsView);
		await expect.element(page.getByRole('status')).toHaveTextContent(m.loading());
		release(listing());
		await expect.element(page.getByRole('heading', { name: 'No active alerts' })).toBeVisible();
		await view.unmount();

		render(AlertsView);
		await expect.element(page.getByRole('alert')).toHaveTextContent(m.error_unavailable());
		await page.getByRole('button', { name: 'Try again' }).click();
		await expect.element(page.getByRole('heading', { name: 'No active alerts' })).toBeVisible();
		expect(list).toHaveBeenCalledTimes(3);
	});
});
