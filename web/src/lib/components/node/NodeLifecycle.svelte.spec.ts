import { create } from '@bufbuild/protobuf';
import { timestampFromMs } from '@bufbuild/protobuf/wkt';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page, userEvent } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { api } from '#lib/api/client.js';
import {
	CancelNodeDeletionResponseSchema,
	DeactivateNodeResponseSchema,
	DeleteNodeResponseSchema,
	NodeSchema
} from '#lib/api/gen/sermon/web/v1/node_pb.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { sessionState } from '#lib/stores/session.svelte.js';
import { stepUpState } from '#lib/stores/stepup.svelte.js';
import { fakeSession, stepUpError } from '#lib/test/fakes.js';
import NodeLifecycle from './NodeLifecycle.svelte';

const now = Date.UTC(2026, 10, 26, 12, 0, 0);
const group = '0193f5a0-0000-7000-8000-0000000000c1';
const nodeId = '0193f5a0-0000-7000-8000-0000000000d1';

function node(over: Partial<{ deactivated: boolean; due: boolean; hostname: string }> = {}) {
	return create(NodeSchema, {
		id: nodeId,
		hostname: over.hostname ?? 'web-01',
		groupId: group,
		deactivatedAt: over.deactivated || over.due ? timestampFromMs(now - 3_600_000) : undefined,
		deletionDueAt: over.due ? timestampFromMs(now + 7 * 86_400_000) : undefined
	});
}

function rights(groups: string[]) {
	sessionState.current = { ...fakeSession(), lifecycleGroupIds: groups };
	sessionState.status = 'authenticated';
}

describe('NodeLifecycle', () => {
	beforeEach(() => switchLocale('en'));
	afterEach(() => {
		vi.restoreAllMocks();
		if (stepUpState.open) stepUpState.finish(false);
		sessionState.current = undefined;
		sessionState.status = 'unknown';
	});

	it('shows the state to everyone, the actions only to admins of the group', async () => {
		rights(['0193f5a0-0000-7000-8000-0000000000ff']);
		render(NodeLifecycle, { node: node({ due: true }), now: new Date(now), onchange: () => {} });
		await expect
			.element(page.getByRole('status'))
			.toMatchTextContent(/Deleted with all its data on/);
		expect(page.getByRole('button').elements()).toHaveLength(0);
	});

	it('deactivates after a warning and the step-up, then reloads', async () => {
		rights([group]);
		const deactivate = vi
			.spyOn(api.node, 'deactivateNode')
			.mockRejectedValueOnce(stepUpError())
			.mockResolvedValueOnce(create(DeactivateNodeResponseSchema, {}));
		const changed = vi.fn();
		render(NodeLifecycle, { node: node(), now: new Date(now), onchange: changed });
		await expect.element(page.getByRole('status')).toMatchTextContent(/Active/);

		await page.getByRole('button', { name: 'Deactivate' }).click();
		expect(deactivate).not.toHaveBeenCalled();
		await expect.element(page.getByRole('alert')).toMatchTextContent(/Allow takeover/);
		await page.getByRole('button', { name: 'Really deactivate' }).click();
		await vi.waitFor(() => expect(stepUpState.open).toBe(true));
		stepUpState.finish(true);
		await vi.waitFor(() => expect(deactivate).toHaveBeenCalledTimes(2));
		expect(deactivate.mock.calls[1][0]).toEqual({ nodeId });
		await vi.waitFor(() => expect(changed).toHaveBeenCalledOnce());
		await expect.element(page.getByText('Deactivated.')).toBeVisible();
	});

	it('deletes only with the host name typed', async () => {
		rights(['*']);
		const due = timestampFromMs(now + 7 * 86_400_000);
		const del = vi
			.spyOn(api.node, 'deleteNode')
			.mockResolvedValue(create(DeleteNodeResponseSchema, { deletionDueAt: due }));
		const changed = vi.fn();
		render(NodeLifecycle, {
			node: node({ deactivated: true }),
			now: new Date(now),
			onchange: changed
		});
		await expect.element(page.getByRole('status')).toMatchTextContent(/Deactivated/);
		// Deactivated already: no second deactivation offered.
		expect(page.getByRole('button', { name: 'Deactivate' }).elements()).toHaveLength(0);

		await page.getByRole('button', { name: 'Delete …' }).click();
		const confirm = page.getByRole('button', { name: 'Delete node' });
		await expect.element(confirm).toBeDisabled();
		const input = page.getByRole('textbox', { name: 'Type the host name web-01 to confirm' });
		await userEvent.fill(input, 'web-02');
		await expect.element(confirm).toBeDisabled();
		await userEvent.fill(input, ' web-01 ');
		await expect.element(confirm).toBeEnabled();
		await confirm.click();
		await vi.waitFor(() => expect(del).toHaveBeenCalledOnce());
		expect(del.mock.calls[0][0]).toEqual({ nodeId, confirmHostname: 'web-01' });
		await vi.waitFor(() => expect(changed).toHaveBeenCalledOnce());
		await expect.element(page.getByText(/The node will be deleted on/)).toBeVisible();
	});

	it('cancels a scheduled deletion', async () => {
		rights([group]);
		const cancel = vi
			.spyOn(api.node, 'cancelNodeDeletion')
			.mockResolvedValue(create(CancelNodeDeletionResponseSchema, {}));
		render(NodeLifecycle, { node: node({ due: true }), now: new Date(now), onchange: () => {} });
		expect(page.getByRole('button', { name: 'Delete …' }).elements()).toHaveLength(0);
		await page.getByRole('button', { name: 'Cancel deletion' }).click();
		await vi.waitFor(() => expect(cancel).toHaveBeenCalledWith({ nodeId }));
		await expect.element(page.getByText(/Deletion cancelled/)).toBeVisible();
	});

	it('names a failure', async () => {
		rights([group]);
		vi.spyOn(api.node, 'deactivateNode').mockRejectedValue(new Error('boom'));
		render(NodeLifecycle, { node: node(), now: new Date(now), onchange: () => {} });
		await page.getByRole('button', { name: 'Deactivate' }).click();
		await page.getByRole('button', { name: 'Really deactivate' }).click();
		await expect
			.poll(() => page.getByRole('alert').elements().at(-1)?.textContent ?? '')
			.not.toBe('');
	});
});
