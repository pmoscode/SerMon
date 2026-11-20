import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { api } from '#lib/api/client.js';
import { Role } from '#lib/api/gen/sermon/web/v1/access_pb.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import InvitationsPanel from './InvitationsPanel.svelte';

describe('InvitationsPanel', () => {
	beforeEach(() => switchLocale('en'));
	afterEach(() => vi.restoreAllMocks());

	it('shows the link once with the token in the fragment', async () => {
		const invite = vi
			.spyOn(api.access, 'inviteMember')
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

	it('withdraws an open invitation', async () => {
		const revoke = vi.spyOn(api.access, 'revokeInvitation').mockResolvedValue({} as never);
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
