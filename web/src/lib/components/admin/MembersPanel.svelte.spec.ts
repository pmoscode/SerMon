import { create } from '@bufbuild/protobuf';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { Code } from '#lib/api/client.js';
import { adminApi } from '#lib/api/admin.js';
import {
	MemberSchema,
	NodeGroupSchema,
	Role,
	RoleBindingSchema
} from '#lib/api/gen/sermon/web/v1/access_pb.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { m } from '#lib/paraglide/messages.js';
import { sessionState } from '#lib/stores/session.svelte.js';
import { stepUpState } from '#lib/stores/stepup.svelte.js';
import { fakeSession, rpcError, stepUpError } from '#lib/test/fakes.js';
import MembersPanel from './MembersPanel.svelte';

const web = create(NodeGroupSchema, { id: 'g1', name: 'web' });
const members = [
	create(MemberSchema, {
		userId: '0193f5a0-0000-7000-8000-0000000000ff',
		email: 'mia@example.org',
		displayName: 'Mia',
		bindings: [create(RoleBindingSchema, { id: 'b1', groupId: '', role: Role.ADMIN })]
	}),
	create(MemberSchema, {
		userId: 'u2',
		email: 'kai@example.org',
		displayName: 'Kai',
		bindings: [create(RoleBindingSchema, { id: 'b2', groupId: 'g1', role: Role.VIEWER })]
	})
];

describe('MembersPanel', () => {
	beforeEach(() => {
		switchLocale('en');
		sessionState.current = fakeSession();
	});
	afterEach(() => {
		vi.restoreAllMocks();
		if (stepUpState.open) stepUpState.finish(false);
		sessionState.current = undefined;
	});

	it('lists members with their roles and marks the caller', async () => {
		render(MembersPanel, { members, groups: [web], onChange: vi.fn() });

		await expect.element(page.getByText('Mia (you)')).toBeVisible();
		await expect.element(page.getByText('Admin · All groups')).toBeVisible();
		await expect.element(page.getByText('Viewer · web')).toBeVisible();
	});

	it('grants a role after the step-up and reloads', async () => {
		const grant = vi
			.spyOn(adminApi.access, 'grantRole')
			.mockRejectedValueOnce(stepUpError())
			.mockResolvedValueOnce({} as never);
		const onChange = vi.fn().mockResolvedValue(undefined);
		render(MembersPanel, { members, groups: [web], onChange });

		await page.getByRole('button', { name: 'Grant role' }).nth(1).click();
		await page.getByLabelText('Role').selectOptions('Operator');
		await page.getByLabelText('Applies to').selectOptions('web');
		await page.getByRole('button', { name: 'Grant', exact: true }).click();
		await vi.waitFor(() => expect(stepUpState.open).toBe(true));
		stepUpState.finish(true);

		await vi.waitFor(() => expect(onChange).toHaveBeenCalledOnce());
		expect(grant).toHaveBeenCalledTimes(2);
		expect(grant.mock.calls[1][0]).toEqual({ userId: 'u2', groupId: 'g1', role: Role.OPERATOR });
	});

	it('revokes a role and explains the last admin', async () => {
		const revoke = vi
			.spyOn(adminApi.access, 'revokeRole')
			.mockRejectedValue(rpcError(Code.FailedPrecondition));
		render(MembersPanel, { members, groups: [web], onChange: vi.fn() });

		await page.getByRole('button', { name: 'Revoke Admin on All groups' }).click();

		await expect.element(page.getByRole('alert')).toHaveTextContent(m.admin_error_last_admin());
		expect(revoke.mock.calls[0][0]).toEqual({ bindingId: 'b1' });
	});

	it('removes a member only after a second click', async () => {
		const remove = vi.spyOn(adminApi.access, 'removeMember').mockResolvedValue({} as never);
		const onChange = vi.fn().mockResolvedValue(undefined);
		render(MembersPanel, { members, groups: [web], onChange });

		await page.getByRole('button', { name: 'Remove', exact: true }).nth(1).click();
		expect(remove).not.toHaveBeenCalled();
		await page.getByRole('button', { name: 'Really remove' }).click();

		await vi.waitFor(() => expect(remove).toHaveBeenCalledWith({ userId: 'u2' }));
	});
});
