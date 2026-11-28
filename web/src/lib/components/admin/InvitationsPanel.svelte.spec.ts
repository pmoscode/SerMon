import { Code, ConnectError } from '@connectrpc/connect';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { adminApi } from '#lib/api/admin.js';
import { Role } from '#lib/api/gen/sermon/web/v1/access_pb.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import InvitationsPanel from './InvitationsPanel.svelte';

describe('InvitationsPanel', () => {
	beforeEach(() => switchLocale('en'));
	afterEach(() => vi.restoreAllMocks());

	it('shows the link once with the token in the fragment', async () => {
		const invite = vi
			.spyOn(adminApi.access, 'inviteMember')
			.mockResolvedValue({ token: 'smi_secret' } as never);
		const onChange = vi.fn().mockResolvedValue(undefined);
		render(InvitationsPanel, { invitations: [], groups: [], onChange });

		await expect.element(page.getByText('No open invitations.')).toBeVisible();
		await page.getByLabelText('Email address').fill('new@example.org');
		await page.getByLabelText('Role').selectOptions('Operator');
		await page.getByRole('button', { name: 'Invite' }).click();

		await expect.element(page.getByText(`${location.origin}/invite#smi_secret`)).toBeVisible();
		expect(invite.mock.calls[0][0]).toEqual({
			email: 'new@example.org',
			role: Role.OPERATOR,
			groupId: ''
		});
		expect(onChange).toHaveBeenCalledOnce();
	});

	// Production: the link went to the invited address only (M1-39) – nothing to copy here.
	it('confirms a mailed invitation without showing a link', async () => {
		vi.spyOn(adminApi.access, 'inviteMember').mockResolvedValue({
			token: '',
			emailed: true
		} as never);
		render(InvitationsPanel, {
			invitations: [],
			groups: [],
			onChange: vi.fn().mockResolvedValue(undefined)
		});

		await page.getByLabelText('Email address').fill('new@example.org');
		await page.getByRole('button', { name: 'Invite' }).click();

		await expect
			.element(page.getByText('Invitation sent to new@example.org', { exact: false }))
			.toBeVisible();
		expect(document.body.textContent).not.toContain('/invite#');
	});

	it('sends an open invitation again with its role and group', async () => {
		const invite = vi
			.spyOn(adminApi.access, 'inviteMember')
			.mockResolvedValue({ token: '', emailed: true } as never);
		render(InvitationsPanel, {
			invitations: [
				{ id: 'i1', email: 'late@example.org', role: Role.OPERATOR, groupId: 'g1' } as never
			],
			groups: [{ id: 'g1', name: 'web' } as never],
			onChange: vi.fn().mockResolvedValue(undefined)
		});

		await page.getByRole('button', { name: 'Send again' }).click();

		await vi.waitFor(() =>
			expect(invite).toHaveBeenCalledWith({
				email: 'late@example.org',
				role: Role.OPERATOR,
				groupId: 'g1'
			})
		);
		await expect
			.element(page.getByText('Invitation sent to late@example.org', { exact: false }))
			.toBeVisible();
	});

	// V1-18: without a working mail server the invitation can't go out (ADR 0022); the admin
	// learns that, not that the server is unreachable.
	it('says when the invitation mail could not be sent', async () => {
		vi.spyOn(adminApi.access, 'inviteMember').mockRejectedValue(
			new ConnectError('mail', Code.Unavailable)
		);
		render(InvitationsPanel, { invitations: [], groups: [], onChange: vi.fn() });
		await page.getByLabelText('Email address').fill('new@example.org');
		await page.getByRole('button', { name: 'Invite' }).click();
		const alert = page.getByRole('alert');
		await expect.element(alert).toBeVisible();
		expect(alert.element().textContent).toContain('could not be sent by mail');
	});

	it('withdraws an open invitation', async () => {
		const revoke = vi.spyOn(adminApi.access, 'revokeInvitation').mockResolvedValue({} as never);
		render(InvitationsPanel, {
			invitations: [
				{ id: 'i1', email: 'late@example.org', role: Role.VIEWER, groupId: '' } as never
			],
			groups: [],
			onChange: vi.fn().mockResolvedValue(undefined)
		});

		await expect.element(page.getByText('late@example.org')).toBeVisible();
		await page.getByRole('button', { name: 'Withdraw' }).click();
		await vi.waitFor(() => expect(revoke).toHaveBeenCalledWith({ invitationId: 'i1' }));
	});
});
