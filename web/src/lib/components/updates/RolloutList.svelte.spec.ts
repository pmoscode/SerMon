import { create } from '@bufbuild/protobuf';
import { Code, ConnectError } from '@connectrpc/connect';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import {
	CancelRolloutResponseSchema,
	RolloutNodeState,
	RolloutSchema,
	RolloutState
} from '#lib/api/gen/sermon/web/v1/update_pb.js';
import { updatesApi } from '#lib/api/updates.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { sessionState } from '#lib/stores/session.svelte.js';
import { stepUpState } from '#lib/stores/stepup.svelte.js';
import { fakeSession, stepUpError } from '#lib/test/fakes.js';
import RolloutList from './RolloutList.svelte';

const groupA = '0193f5a0-0000-7000-8000-0000000000c1';
const groupB = '0193f5a0-0000-7000-8000-0000000000c2';
const running = '0193f5a0-0000-7000-8000-0000000000b1';

const rollouts = () => [
	create(RolloutSchema, {
		rolloutId: running,
		groupId: groupA,
		groupName: 'web',
		manual: true,
		state: RolloutState.RUNNING,
		nodes: [
			{ nodeId: 'n1', hostname: 'web-1', state: RolloutNodeState.UPDATING },
			{ nodeId: 'n2', hostname: 'web-2', state: RolloutNodeState.WAITING }
		]
	}),
	create(RolloutSchema, {
		rolloutId: 'b2',
		groupId: groupA,
		groupName: 'web',
		state: RolloutState.DONE
	}),
	create(RolloutSchema, {
		rolloutId: 'b3',
		groupId: groupB,
		groupName: 'db',
		state: RolloutState.RUNNING
	})
];

function rights(update: string[]) {
	sessionState.current = { ...fakeSession(), runUpdatesGroupIds: update };
	sessionState.status = 'authenticated';
}

const cancelButtons = () => page.getByRole('button', { name: 'Cancel', exact: true });

describe('RolloutList', () => {
	beforeEach(() => switchLocale('en'));
	afterEach(() => {
		vi.restoreAllMocks();
		if (stepUpState.open) stepUpState.finish(false);
		sessionState.current = undefined;
		sessionState.status = 'unknown';
	});

	it('lists the rollouts with their nodes', async () => {
		rights([]);
		render(RolloutList, { rollouts: rollouts(), onchange: async () => {} });
		await expect.element(page.getByText('web-1: updating', { exact: false })).toBeVisible();
		expect(document.body.textContent).toContain('web-2: waiting');
		expect(page.getByRole('listitem').elements()).toHaveLength(3);
	});

	it('says when there are no rollouts', async () => {
		rights([groupA]);
		render(RolloutList, { rollouts: [], onchange: async () => {} });
		await expect.element(page.getByText('No rollouts yet.')).toBeVisible();
	});

	it('offers to cancel only running rollouts of groups with the right', async () => {
		rights([groupA]);
		const first = await render(RolloutList, { rollouts: rollouts(), onchange: async () => {} });
		await expect.element(page.getByText('web-1: updating', { exact: false })).toBeVisible();
		expect(cancelButtons().elements()).toHaveLength(1);
		expect(
			page.getByRole('listitem').filter({ hasText: 'web-1' }).getByRole('button').elements()
		).toHaveLength(1);
		await first.unmount();

		rights(['*']);
		const second = await render(RolloutList, { rollouts: rollouts(), onchange: async () => {} });
		await expect.element(cancelButtons().first()).toBeVisible();
		expect(cancelButtons().elements()).toHaveLength(2);
		await second.unmount();

		rights([]);
		render(RolloutList, { rollouts: rollouts(), onchange: async () => {} });
		await expect.element(page.getByText('web-1: updating', { exact: false })).toBeVisible();
		expect(cancelButtons().elements()).toHaveLength(0);
	});

	it('cancels after a confirmation and a step-up, then reloads', async () => {
		rights([groupA]);
		const onchange = vi.fn(async () => {});
		const cancel = vi
			.spyOn(updatesApi.updates, 'cancelRollout')
			.mockRejectedValueOnce(stepUpError())
			.mockResolvedValueOnce(create(CancelRolloutResponseSchema, {}));
		render(RolloutList, { rollouts: rollouts(), onchange });
		await cancelButtons().click();
		const confirm = page.getByRole('group', { name: 'Cancel this rollout?' });
		expect(confirm.element().textContent).toContain('Nodes still waiting are skipped');
		expect(confirm.element().textContent).toContain('finish without a reboot');
		await page.getByRole('button', { name: 'Keep running' }).click();
		await expect.element(confirm).not.toBeInTheDocument();
		expect(cancel).not.toHaveBeenCalled();

		await cancelButtons().click();
		await page.getByRole('button', { name: 'Cancel rollout' }).click();
		await vi.waitFor(() => expect(stepUpState.open).toBe(true));
		stepUpState.finish(true);
		await vi.waitFor(() => expect(cancel).toHaveBeenCalledTimes(2));
		expect(cancel.mock.calls[1][0]).toEqual({ rolloutId: running });
		await vi.waitFor(() => expect(onchange).toHaveBeenCalled());
		await expect.element(confirm).not.toBeInTheDocument();
	});

	it('says when the rollout has ended already', async () => {
		rights([groupA]);
		const onchange = vi.fn(async () => {});
		vi.spyOn(updatesApi.updates, 'cancelRollout').mockRejectedValue(
			new ConnectError('ended', Code.FailedPrecondition)
		);
		render(RolloutList, { rollouts: rollouts(), onchange });
		await cancelButtons().click();
		await page.getByRole('button', { name: 'Cancel rollout' }).click();
		await expect.element(page.getByRole('alert')).toMatchTextContent(/already ended/);
		expect(onchange).toHaveBeenCalled();
	});
});
