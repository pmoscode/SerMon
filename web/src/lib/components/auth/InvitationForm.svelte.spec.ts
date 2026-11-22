import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { Code } from '#lib/api/client.js';
import { onboardingApi } from '#lib/api/onboarding.js';
import { Role } from '#lib/api/gen/sermon/web/v1/access_pb.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { m } from '#lib/paraglide/messages.js';
import { sessionState } from '#lib/stores/session.svelte.js';
import { fakeSession, PASSPHRASE, rpcError } from '#lib/test/fakes.js';
import InvitationForm from './InvitationForm.svelte';

const invitation = { organizationName: 'Alpha', email: 'new@example.org', role: Role.OPERATOR };

describe('InvitationForm', () => {
	beforeEach(() => switchLocale('en'));
	afterEach(() => {
		vi.restoreAllMocks();
		sessionState.status = 'unknown';
		sessionState.current = undefined;
	});

	it('creates an account for the invited address', async () => {
		sessionState.status = 'anonymous';
		vi.spyOn(onboardingApi.invitation, 'getInvitation').mockResolvedValue(invitation as never);
		const accept = vi
			.spyOn(onboardingApi.invitation, 'acceptInvitation')
			.mockResolvedValue({} as never);
		const onAccepted = vi.fn();
		render(InvitationForm, { token: 'smi_x', onAccepted, onJoined: vi.fn() });

		await expect
			.element(page.getByText('Alpha invites new@example.org as Operator.'))
			.toBeVisible();
		await page.getByRole('button', { name: 'Create account and join' }).click();
		await expect.element(page.getByLabelText('Name')).toHaveAttribute('aria-invalid', 'true');
		expect(accept).not.toHaveBeenCalled();

		await page.getByLabelText('Name').fill('Neu');
		await page.getByLabelText('Password', { exact: true }).fill(PASSPHRASE);
		await page.getByLabelText('Repeat password').fill(PASSPHRASE);
		await page.getByRole('button', { name: 'Create account and join' }).click();

		await vi.waitFor(() => expect(onAccepted).toHaveBeenCalledOnce());
		expect(accept.mock.calls[0][0]).toEqual({
			token: 'smi_x',
			displayName: 'Neu',
			password: PASSPHRASE
		});
	});

	it('lets a logged-in user join and switches to the organization', async () => {
		sessionState.status = 'authenticated';
		sessionState.current = fakeSession();
		vi.spyOn(onboardingApi.invitation, 'getInvitation').mockResolvedValue(invitation as never);
		vi.spyOn(onboardingApi.invitation, 'joinOrganization').mockResolvedValue({
			organizationId: 'o2'
		} as never);
		const switchOrg = vi
			.spyOn(sessionState, 'switchOrganization')
			.mockResolvedValue(undefined as never);
		const onJoined = vi.fn();
		render(InvitationForm, { token: 'smi_x', onAccepted: vi.fn(), onJoined });

		await expect.element(page.getByText('Signed in as mia@example.org.')).toBeVisible();
		await page.getByRole('button', { name: 'Join' }).click();

		await vi.waitFor(() => expect(onJoined).toHaveBeenCalledOnce());
		expect(switchOrg).toHaveBeenCalledWith('o2');
	});

	it('says so for an invalid link', async () => {
		sessionState.status = 'anonymous';
		vi.spyOn(onboardingApi.invitation, 'getInvitation').mockRejectedValue(
			rpcError(Code.Unauthenticated)
		);
		render(InvitationForm, { token: 'smi_x', onAccepted: vi.fn(), onJoined: vi.fn() });

		await expect.element(page.getByRole('alert')).toHaveTextContent(m.invite_invalid());
	});
});
