import { create } from '@bufbuild/protobuf';
import { timestampFromMs } from '@bufbuild/protobuf/wkt';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { api } from '#lib/api/client.js';
import {
	AllowNodeTakeoverResponseSchema,
	NodeSchema,
	RotateNodeKeysResponseSchema,
	UnlockNodeResponseSchema
} from '#lib/api/gen/sermon/web/v1/node_pb.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { sessionState } from '#lib/stores/session.svelte.js';
import { stepUpState } from '#lib/stores/stepup.svelte.js';
import { fakeSession, stepUpError } from '#lib/test/fakes.js';
import NodeIdentity from './NodeIdentity.svelte';

const now = Date.UTC(2026, 10, 22, 12, 0, 0);
const group = '0193f5a0-0000-7000-8000-0000000000c1';
const nodeId = '0193f5a0-0000-7000-8000-0000000000d1';

function node(over: Partial<{ locked: boolean; takeover: boolean }> = {}) {
	return create(NodeSchema, {
		id: nodeId,
		hostname: 'web-01',
		groupId: group,
		lastIp: '192.0.2.10',
		lockedAt: over.locked ? timestampFromMs(now - 3_600_000) : undefined,
		lockReason: over.locked ? 'second session from 192.0.2.66' : '',
		takeoverUntil: over.takeover ? timestampFromMs(now + 1_800_000) : undefined
	});
}

function rights(groups: string[]) {
	sessionState.current = { ...fakeSession(), nodeIdentityGroupIds: groups };
	sessionState.status = 'authenticated';
}

describe('NodeIdentity', () => {
	beforeEach(() => switchLocale('en'));
	afterEach(() => {
		vi.restoreAllMocks();
		if (stepUpState.open) stepUpState.finish(false);
		sessionState.current = undefined;
		sessionState.status = 'unknown';
	});

	it('shows lock, reason and last address to everyone, actions only with the right', async () => {
		rights([]);
		render(NodeIdentity, { node: node({ locked: true }), now: new Date(now), onchange: () => {} });
		await expect.element(page.getByRole('status')).toMatchTextContent(/Locked/);
		await expect
			.element(page.getByText('second session from 192.0.2.66', { exact: false }))
			.toBeVisible();
		await expect.element(page.getByText('192.0.2.10')).toBeVisible();
		expect(page.getByRole('button').elements()).toHaveLength(0);
	});

	it('unlocks after a confirmation and the step-up, then reloads', async () => {
		rights([group]);
		const unlock = vi
			.spyOn(api.node, 'unlockNode')
			.mockRejectedValueOnce(stepUpError())
			.mockResolvedValueOnce(create(UnlockNodeResponseSchema, {}));
		const changed = vi.fn();
		render(NodeIdentity, { node: node({ locked: true }), now: new Date(now), onchange: changed });

		await page.getByRole('button', { name: 'Unlock' }).click();
		expect(unlock).not.toHaveBeenCalled();
		// The warning names the alternative for a stolen key.
		await expect.element(page.getByRole('alert')).toMatchTextContent(/revoke/i);
		await page.getByRole('button', { name: 'Really unlock' }).click();
		await vi.waitFor(() => expect(stepUpState.open).toBe(true));
		stepUpState.finish(true);
		await vi.waitFor(() => expect(unlock).toHaveBeenCalledTimes(2));
		expect(unlock.mock.calls[1][0]).toEqual({ nodeId });
		await vi.waitFor(() => expect(changed).toHaveBeenCalledOnce());
	});

	it('revokes the key only after a confirmation and rotates on request', async () => {
		rights(['*']);
		const rotate = vi
			.spyOn(api.node, 'rotateNodeKeys')
			.mockResolvedValue(create(RotateNodeKeysResponseSchema, { online: 1 }));
		render(NodeIdentity, { node: node(), now: new Date(now), onchange: () => {} });
		await expect.element(page.getByRole('status')).toMatchTextContent(/Not locked/);

		await page.getByRole('button', { name: 'Rotate key' }).click();
		await vi.waitFor(() =>
			expect(rotate).toHaveBeenCalledWith({
				target: { case: 'nodes', value: { ids: [nodeId] } },
				revokeOnly: false
			})
		);
		await expect.element(page.getByText(/new key/)).toBeVisible();

		await page.getByRole('button', { name: 'Revoke key' }).click();
		expect(rotate).toHaveBeenCalledOnce();
		await page.getByRole('button', { name: 'Really revoke' }).click();
		await vi.waitFor(() => expect(rotate).toHaveBeenCalledTimes(2));
		expect(rotate.mock.calls[1][0]).toMatchObject({ revokeOnly: true });
	});

	it('allows a takeover and shows the open window', async () => {
		rights([group]);
		const allow = vi
			.spyOn(api.node, 'allowNodeTakeover')
			.mockResolvedValue(
				create(AllowNodeTakeoverResponseSchema, { until: timestampFromMs(now + 3_600_000) })
			);
		const changed = vi.fn();
		render(NodeIdentity, { node: node(), now: new Date(now), onchange: changed });
		await page.getByRole('button', { name: 'Allow takeover' }).click();
		await vi.waitFor(() => expect(allow).toHaveBeenCalledWith({ nodeId }));
		await vi.waitFor(() => expect(changed).toHaveBeenCalled());

		// With an open window the node says until when; the button is gone.
		render(NodeIdentity, {
			node: node({ takeover: true }),
			now: new Date(now),
			onchange: () => {}
		});
		await expect.element(page.getByText(/Takeover allowed for/)).toBeVisible();
	});
});
