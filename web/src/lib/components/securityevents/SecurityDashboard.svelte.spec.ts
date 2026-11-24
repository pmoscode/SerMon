import { create, type MessageInitShape } from '@bufbuild/protobuf';
import {
	durationFromMs,
	timestampFromMs,
	timestampMs,
	type Timestamp
} from '@bufbuild/protobuf/wkt';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { api, Code } from '#lib/api/client.js';
import { GetFleetSummaryResponseSchema } from '#lib/api/gen/sermon/web/v1/fleet_pb.js';
import {
	GetSecurityOverviewResponseSchema,
	ListSecurityEventsResponseSchema,
	SecurityEventSchema
} from '#lib/api/gen/sermon/web/v1/security_event_pb.js';
import { securityApi } from '#lib/api/security.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { emptySecurityFilter, type SecurityFilter } from '#lib/security/filter.js';
import { rpcError } from '#lib/test/fakes.js';
import SecurityDashboard from './SecurityDashboard.svelte';

const web01 = '0193f5a0-0000-7000-8000-000000000001';
const db01 = '0193f5a0-0000-7000-8000-000000000002';
const now = Date.now();
const hostile = '<img src=x onerror=alert(1)>\u202Etoor';

function event(extra: Record<string, unknown>) {
	const init = {
		id: 1n,
		nodeId: web01,
		occurredAt: timestampFromMs(now - 60_000),
		kind: 'ssh.login.failed',
		user: 'root',
		remoteIp: '192.0.2.66',
		remotePort: 50022,
		details: { method: 'password' },
		...extra
	} as MessageInitShape<typeof SecurityEventSchema>;
	return create(SecurityEventSchema, init);
}

function stubFleet() {
	return vi.spyOn(api.fleet, 'getFleetSummary').mockResolvedValue(
		create(GetFleetSummaryResponseSchema, {
			nodes: [
				{ nodeId: web01, hostname: 'web-01', groupId: 'g1', groupName: 'web' },
				{ nodeId: db01, hostname: 'db-01', groupId: 'g1', groupName: 'web' }
			]
		})
	);
}

function stubOverview() {
	return vi.spyOn(securityApi.events, 'getSecurityOverview').mockResolvedValue(
		create(GetSecurityOverviewResponseSchema, {
			step: durationFromMs(15 * 60_000),
			failedLogins: [
				{ nodeId: web01, points: [{ at: timestampFromMs(now - 3600_000), count: 40n }] },
				{ nodeId: db01, points: [{ at: timestampFromMs(now - 1800_000), count: 2n }] }
			],
			topIps: [
				{ value: '192.0.2.66', count: 40n, nodes: 2, lastAt: timestampFromMs(now), unverified: 3n }
			],
			topUsers: [
				{ value: hostile, count: 30n, nodes: 1, lastAt: timestampFromMs(now) },
				{ value: '', count: 12n, nodes: 1, lastAt: timestampFromMs(now) }
			],
			totals: {
				failedLogins: 42n,
				logins: 7n,
				newSourceLogins: 2n,
				rootLogins: 1n,
				sudoFailures: 3n
			}
		})
	);
}

function stubList(...events: ReturnType<typeof event>[]) {
	return vi
		.spyOn(securityApi.events, 'listSecurityEvents')
		.mockResolvedValue(create(ListSecurityEventsResponseSchema, { events }));
}

function show(filter: SecurityFilter = emptySecurityFilter(), fixedNode?: string) {
	const onFilter = vi.fn();
	render(SecurityDashboard, { filter, onFilter, fixedNode });
	return onFilter;
}

describe('SecurityDashboard', () => {
	beforeEach(() => switchLocale('en'));
	afterEach(() => vi.restoreAllMocks());

	it('shows totals, chart, top lists and events, host texts as text only', async () => {
		stubFleet();
		stubOverview();
		stubList(
			event({ id: 2n, user: hostile, unverified: true }),
			event({
				id: 3n,
				kind: 'ssh.login.success',
				user: 'alice',
				newSource: true,
				details: { method: 'publickey', keyFingerprint: 'SHA256:abc' }
			}),
			event({
				id: 4n,
				kind: 'sudo.command',
				user: 'bob',
				remoteIp: '',
				remotePort: 0,
				details: { target: 'root', command: 'rm -rf /tmp/x\u202E' }
			}),
			event({
				id: 5n,
				kind: 'ssh.preauth.disconnect',
				remoteIp: '',
				remoteHost: 'scan\u202Eer.example',
				remotePort: 0
			})
		);
		show();
		await expect.element(page.getByText('42', { exact: true })).toBeVisible();
		await expect.element(page.getByText('2 of them from new sources')).toBeVisible();
		await expect.element(page.getByRole('figure')).toMatchTextContent(/web-01\s*40.*db-01\s*2/);
		await expect.element(page.getByText('3 unverified')).toBeVisible();
		await expect.element(page.getByText('(empty)').first()).toBeVisible();

		const table = page.getByRole('table');
		await expect.element(table).toMatchTextContent(/web-01.*SSH failed login/);
		const user = table.getByText('toor', { exact: false });
		expect(user.element().textContent).toBe('<img src=x onerror=alert(1)>⟨U+202E⟩toor');
		await expect.element(table.getByText('unverified', { exact: true })).toBeVisible();
		await expect.element(table.getByText('new', { exact: true })).toBeVisible();
		await expect.element(table.getByText('192.0.2.66:50022').first()).toBeVisible();
		await expect
			.element(table.getByText(/as root · command rm -rf \/tmp\/x⟨U\+202E⟩/))
			.toBeVisible();
		await expect.element(table.getByText('scan⟨U+202E⟩er.example')).toBeVisible();
		expect(document.querySelector('img[src="x"]')).toBeNull();
		expect(document.body.textContent).not.toContain('\u202E');

		const logs = table.getByRole('link', { name: /Logs around/ }).first();
		expect(logs.element().getAttribute('href')).toMatch(
			new RegExp(`^/logs\\?from=.*&node=${web01}$`)
		);
	});

	it('asks the overview and the list for the range and filter', async () => {
		stubFleet();
		const overview = stubOverview();
		const list = stubList();
		const filter: SecurityFilter = {
			...emptySecurityFilter(),
			preset: '7d',
			kinds: ['ssh.login.success'],
			user: 'root',
			network: '192.0.2.0/24',
			newOnly: true
		};
		show(filter, db01);
		await expect.element(page.getByText('No events for this filter.')).toBeVisible();
		const o = overview.mock.calls[0][0];
		expect(o.nodeIds).toEqual([db01]);
		expect(timestampMs(o.to as Timestamp) - timestampMs(o.from as Timestamp)).toBe(7 * 86400_000);
		expect(list.mock.calls[0][0]).toMatchObject({
			nodeIds: [db01],
			kinds: ['ssh.login.success'],
			user: 'root',
			network: '192.0.2.0/24',
			newSourcesOnly: true,
			pageSize: 100
		});
		// On a node page there is no node choice and no node column.
		expect(page.getByLabelText('Node', { exact: true }).elements()).toHaveLength(0);
	});

	it('filters from the totals, the top lists and the form', async () => {
		stubFleet();
		stubOverview();
		stubList(event({}));
		const onFilter = show();
		await page.getByRole('button', { name: 'Show root logins' }).click();
		expect(onFilter).toHaveBeenLastCalledWith(
			expect.objectContaining({ kinds: ['ssh.login.success'], user: 'root', newOnly: false })
		);
		await page.getByRole('button', { name: 'Show new sources' }).click();
		expect(onFilter).toHaveBeenLastCalledWith(
			expect.objectContaining({ kinds: ['ssh.login.success'], newOnly: true })
		);
		await page.getByRole('button', { name: 'Show events of 192.0.2.66' }).click();
		expect(onFilter).toHaveBeenLastCalledWith(
			expect.objectContaining({
				network: '192.0.2.66',
				kinds: ['ssh.login.failed', 'ssh.login.invalid_user']
			})
		);
		await page.getByRole('button', { name: 'Show events of (empty)' }).click();
		expect(onFilter).toHaveBeenLastCalledWith(expect.objectContaining({ user: '' }));

		await page.getByLabelText('IP or network').fill('evil.example');
		await page.getByRole('button', { name: 'Search' }).click();
		await expect
			.element(page.getByText('Neither an address nor a network in CIDR notation.'))
			.toBeVisible();
		const calls = onFilter.mock.calls.length;
		await page.getByLabelText('IP or network').fill('2001:db8::/32');
		await page.getByLabelText('Kind').selectOptions('sudo');
		await page.getByLabelText('User', { exact: true }).fill('alice');
		await page.getByRole('button', { name: 'Search' }).click();
		expect(onFilter).toHaveBeenCalledTimes(calls + 1);
		expect(onFilter).toHaveBeenLastCalledWith(
			expect.objectContaining({
				network: '2001:db8::/32',
				kinds: ['sudo.auth.failed', 'sudo.not_allowed'],
				user: 'alice'
			})
		);
		await page.getByRole('button', { name: '30 days' }).click();
		expect(onFilter).toHaveBeenLastCalledWith(expect.objectContaining({ preset: '30d' }));
	});

	it('loads more and downloads the CSV of the filter', async () => {
		stubFleet();
		stubOverview();
		const list = vi
			.spyOn(securityApi.events, 'listSecurityEvents')
			.mockResolvedValueOnce(
				create(ListSecurityEventsResponseSchema, {
					events: [event({ id: 1n })],
					nextPageToken: 'p2'
				})
			)
			.mockResolvedValueOnce(
				create(ListSecurityEventsResponseSchema, { events: [event({ id: 2n, user: 'second' })] })
			);
		const exported = vi.spyOn(securityApi.events, 'exportSecurityEvents').mockResolvedValue({
			fileName: 'sermon-security-events.csv',
			content: new TextEncoder().encode('occurred_at\r\n'),
			rows: 10000,
			truncated: true
		} as never);
		const blob = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:x');
		vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {});
		const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
		show({ ...emptySecurityFilter(), user: 'root' });
		await expect.element(page.getByText('1 events, more available')).toBeVisible();
		await page.getByRole('button', { name: 'Load older events' }).click();
		await expect.element(page.getByText('second')).toBeVisible();
		expect(list.mock.calls[1][0].pageToken).toBe('p2');

		await page.getByRole('button', { name: 'Export as CSV' }).click();
		await expect.element(page.getByText(/Saved the first 10,000 events as CSV/)).toBeVisible();
		expect(exported.mock.calls[0][0].filter).toMatchObject({ user: 'root' });
		expect(blob).toHaveBeenCalledOnce();
		expect(click).toHaveBeenCalledOnce();
	});

	it('says so without the right', async () => {
		stubFleet();
		vi.spyOn(securityApi.events, 'getSecurityOverview').mockRejectedValue(
			rpcError(Code.PermissionDenied)
		);
		vi.spyOn(securityApi.events, 'listSecurityEvents').mockRejectedValue(
			rpcError(Code.PermissionDenied)
		);
		show();
		await expect
			.element(page.getByRole('alert'))
			.toMatchTextContent(/You lack the right to see security events/);
		await expect
			.element(page.getByText('The overview is not available right now.').first())
			.toBeVisible();
	});
});
