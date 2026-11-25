import { create } from '@bufbuild/protobuf';
import { timestampFromMs } from '@bufbuild/protobuf/wkt';
import { Code, ConnectError } from '@connectrpc/connect';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import {
	GetNodeUpdatesResponseSchema,
	ListUpdateRunsResponseSchema,
	RebootNodesResponseSchema,
	StartUpdatesResponseSchema,
	UpdateAvailability,
	UpdateScope
} from '#lib/api/gen/sermon/web/v1/update_pb.js';
import { UpdateStatus } from '#lib/api/gen/sermon/web/v1/update_status_pb.js';
import { updatesApi } from '#lib/api/updates.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { sessionState } from '#lib/stores/session.svelte.js';
import { stepUpState } from '#lib/stores/stepup.svelte.js';
import { fakeSession, stepUpError } from '#lib/test/fakes.js';
import NodeUpdates from './NodeUpdates.svelte';

const now = Date.UTC(2026, 10, 22, 12, 0, 0);
const group = '0193f5a0-0000-7000-8000-0000000000c1';
const nodeId = '0193f5a0-0000-7000-8000-0000000000d1';

function report(over: { remoteUpdates?: boolean; remoteReboot?: boolean } = {}) {
	return create(GetNodeUpdatesResponseSchema, {
		node: {
			nodeId,
			status: UpdateStatus.SECURITY,
			availability: UpdateAvailability.LISTED,
			totalCount: 3,
			securityCount: 1,
			rebootRequired: true,
			rebootReason: 'linux-image-6.12',
			collectedAt: timestampFromMs(now - 3_600_000),
			permissions: {
				remoteUpdates: over.remoteUpdates ?? true,
				remoteReboot: over.remoteReboot ?? true
			}
		},
		packages: [
			{
				name: 'openssl',
				installedVersion: '3.0.1',
				candidateVersion: '3.0.2',
				source: 'trixie-security',
				security: true
			},
			// From a hostile package source: markup stays text, a bidi override shows up.
			{
				name: '<img src=x onerror=alert(1)>',
				installedVersion: '1',
				candidateVersion: '2',
				source: 'evil\u202eliam'
			},
			{ name: 'vim', installedVersion: '9.1', candidateVersion: '9.2', source: 'trixie' }
		]
	});
}

function rights(update: string[], reboot: string[] = update) {
	sessionState.current = { ...fakeSession(), runUpdatesGroupIds: update, rebootGroupIds: reboot };
	sessionState.status = 'authenticated';
}

function show() {
	render(NodeUpdates, { nodeId, groupId: group, now: new Date(now) });
}

describe('NodeUpdates', () => {
	beforeEach(() => {
		switchLocale('en');
		vi.spyOn(updatesApi.updates, 'listUpdateRuns').mockResolvedValue(
			create(ListUpdateRunsResponseSchema, {})
		);
	});
	afterEach(() => {
		vi.restoreAllMocks();
		if (stepUpState.open) stepUpState.finish(false);
		sessionState.current = undefined;
		sessionState.status = 'unknown';
	});

	it('shows the report with the packages as text, actions only with the right', async () => {
		rights([]);
		vi.spyOn(updatesApi.updates, 'getNodeUpdates').mockResolvedValue(report());
		show();
		await expect
			.element(page.getByRole('status'))
			.toHaveTextContent('1 security updates (3 in all)');
		await expect.element(page.getByText('linux-image-6.12', { exact: false })).toBeVisible();
		await expect.element(page.getByText('<img src=x onerror=alert(1)>')).toBeVisible();
		expect(document.querySelector('img')).toBeNull();
		await expect.element(page.getByText('evil\u202eliam')).not.toBeInTheDocument();
		await expect.element(page.getByText(/^evil.+liam$/)).toBeVisible();
		await expect.element(page.getByText('security', { exact: true })).toBeVisible();
		for (const name of ['Update now', 'List again', 'Reboot']) {
			expect(page.getByRole('button', { name, exact: true }).elements()).toHaveLength(0);
		}
	});

	it('filters the packages by name and to the security updates', async () => {
		rights([]);
		vi.spyOn(updatesApi.updates, 'getNodeUpdates').mockResolvedValue(report());
		show();
		const table = page.getByRole('table', { name: 'Available updates' });
		await expect.element(table.getByRole('cell', { name: 'vim', exact: true })).toBeVisible();
		await page.getByRole('checkbox', { name: 'Security updates only' }).click();
		await expect
			.element(table.getByRole('cell', { name: 'vim', exact: true }))
			.not.toBeInTheDocument();
		await expect.element(table.getByRole('cell', { name: /^openssl/ })).toBeVisible();
		await page.getByRole('checkbox', { name: 'Security updates only' }).click();
		await page.getByLabelText('Filter packages').fill('VI');
		await expect.element(table.getByRole('cell', { name: 'vim', exact: true })).toBeVisible();
		await expect.element(table.getByRole('cell', { name: /^openssl/ })).not.toBeInTheDocument();
	});

	it('updates after a confirmation with the package list and a step-up', async () => {
		rights([group]);
		vi.spyOn(updatesApi.updates, 'getNodeUpdates').mockResolvedValue(report());
		const start = vi
			.spyOn(updatesApi.updates, 'startUpdates')
			.mockRejectedValueOnce(stepUpError())
			.mockResolvedValueOnce(create(StartUpdatesResponseSchema, { rolloutIds: ['r'] }));
		show();
		await page.getByRole('button', { name: 'Update now' }).click();
		expect(start).not.toHaveBeenCalled();
		// The confirmation lists the security update only, by default.
		const confirm = page.getByRole('table', { name: 'These packages will be updated' });
		await expect.element(confirm.getByRole('cell', { name: /^openssl/ })).toBeVisible();
		await expect.element(confirm.getByRole('cell', { name: 'vim' })).not.toBeInTheDocument();
		await page.getByRole('radio', { name: 'All updates (3)' }).click();
		await expect.element(confirm.getByRole('cell', { name: 'vim' })).toBeVisible();
		await page.getByRole('checkbox', { name: 'Reboot afterwards if needed' }).click();
		await page.getByRole('button', { name: 'Update 3 packages' }).click();
		await vi.waitFor(() => expect(stepUpState.open).toBe(true));
		stepUpState.finish(true);
		await vi.waitFor(() => expect(start).toHaveBeenCalledTimes(2));
		expect(start.mock.calls[1][0]).toEqual({
			nodeIds: [nodeId],
			scope: UpdateScope.ALL,
			reboot: true
		});
		await expect.element(page.getByText(/Started/)).toBeVisible();
	});

	it('says what the host forbids and locks the buttons', async () => {
		rights([group]);
		vi.spyOn(updatesApi.updates, 'getNodeUpdates').mockResolvedValue(
			report({ remoteUpdates: false, remoteReboot: false })
		);
		show();
		await expect.element(page.getByText(/allows no remote updates/)).toBeVisible();
		await expect.element(page.getByText(/allows no remote reboot/)).toBeVisible();
		await expect.element(page.getByRole('button', { name: 'Update now' })).toBeDisabled();
		await expect.element(page.getByRole('button', { name: 'Reboot' })).toBeDisabled();
	});

	it('offers no reboot without the reboot right', async () => {
		rights([group], []);
		vi.spyOn(updatesApi.updates, 'getNodeUpdates').mockResolvedValue(report());
		show();
		await page.getByRole('button', { name: 'Update now' }).click();
		await expect
			.element(page.getByRole('checkbox', { name: 'Reboot afterwards if needed' }))
			.toBeDisabled();
		expect(page.getByRole('button', { name: 'Reboot', exact: true }).elements()).toHaveLength(0);
	});

	it('reboots with a reason', async () => {
		rights([group]);
		vi.spyOn(updatesApi.updates, 'getNodeUpdates').mockResolvedValue(report());
		const reboot = vi
			.spyOn(updatesApi.updates, 'rebootNodes')
			.mockResolvedValue(create(RebootNodesResponseSchema, { runIds: ['x'] }));
		show();
		await page.getByRole('button', { name: 'Reboot', exact: true }).click();
		const confirm = page.getByRole('button', { name: 'Reboot in a minute' });
		await expect.element(confirm).toBeDisabled();
		await page.getByLabelText(/Reason/).fill('  kernel 6.12  ');
		await confirm.click();
		await vi.waitFor(() =>
			expect(reboot).toHaveBeenCalledWith({ nodeIds: [nodeId], reason: 'kernel 6.12' })
		);
	});

	it('asks for a new list and says when it is too soon', async () => {
		rights([group]);
		vi.spyOn(updatesApi.updates, 'getNodeUpdates').mockResolvedValue(report());
		vi.spyOn(updatesApi.updates, 'refreshUpdateInfo').mockRejectedValue(
			new ConnectError('too soon', Code.ResourceExhausted)
		);
		show();
		await page.getByRole('button', { name: 'List again' }).click();
		await expect.element(page.getByRole('alert')).toMatchTextContent(/a moment ago/);
	});
});
