import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { api, Code } from '#lib/api/client.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { sessionState } from '#lib/stores/session.svelte.js';
import { fakeSession, rpcError } from '#lib/test/fakes.js';
import OrgSwitcher from './OrgSwitcher.svelte';

describe('OrgSwitcher', () => {
	beforeEach(() => {
		switchLocale('en');
		sessionState.status = 'authenticated';
	});
	afterEach(() => {
		vi.restoreAllMocks();
		sessionState.status = 'unknown';
		sessionState.current = undefined;
	});

	it('shows the only organization as text', async () => {
		sessionState.current = fakeSession('Alpha');
		render(OrgSwitcher);

		await expect.element(page.getByText(/Alpha/)).toBeVisible();
		expect(page.getByRole('combobox').query()).toBeNull();
	});

	it('switches to another organization', async () => {
		sessionState.current = fakeSession('Alpha', 'Beta');
		const beta = fakeSession('Alpha', 'Beta');
		beta.activeOrganizationId = beta.organizations[1].id;
		vi.spyOn(api.auth, 'switchOrganization').mockResolvedValue({ session: beta } as never);
		render(OrgSwitcher);

		await page.getByRole('combobox', { name: 'Organization' }).selectOptions('Beta');

		await vi.waitFor(() => expect(sessionState.organization?.name).toBe('Beta'));
	});

	it('keeps the organization and says so when switching fails', async () => {
		sessionState.current = fakeSession('Alpha', 'Beta');
		vi.spyOn(api.auth, 'switchOrganization').mockRejectedValue(rpcError(Code.NotFound));
		render(OrgSwitcher);

		await page.getByRole('combobox', { name: 'Organization' }).selectOptions('Beta');

		await expect
			.element(page.getByRole('alert'))
			.toHaveTextContent("Couldn't switch the organization.");
		await expect
			.element(page.getByRole('combobox', { name: 'Organization' }))
			.toHaveValue(sessionState.current.organizations[0].id);
	});
});
