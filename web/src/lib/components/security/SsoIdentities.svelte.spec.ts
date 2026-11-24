import { create } from '@bufbuild/protobuf';
import { timestampFromMs } from '@bufbuild/protobuf/wkt';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { api, Code } from '#lib/api/client.js';
import { ListOidcIdentitiesResponseSchema } from '#lib/api/gen/sermon/web/v1/auth_pb.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { sessionState } from '#lib/stores/session.svelte.js';
import { browser, ssoState } from '#lib/stores/sso.svelte.js';
import { stepUpState } from '#lib/stores/stepup.svelte.js';
import { fakeSession, rpcError, stepUpError } from '#lib/test/fakes.js';
import SsoIdentities from './SsoIdentities.svelte';

const hostile = '<img src=x onerror=alert(1)>';
const linked = create(ListOidcIdentitiesResponseSchema, {
	identities: [
		{
			issuer: 'https://id.example/realms/ops',
			subject: 'sub-1',
			email: hostile,
			createTime: timestampFromMs(Date.parse('2026-11-01T10:00:00Z'))
		}
	]
});

describe('SsoIdentities', () => {
	beforeEach(() => {
		switchLocale('en');
		sessionState.status = 'authenticated';
		sessionState.current = fakeSession();
		vi.spyOn(api.auth, 'getLoginOptions').mockResolvedValue({
			oidc: true,
			oidcName: 'Keycloak'
		} as never);
		vi.spyOn(api.auth, 'getSession').mockResolvedValue({ session: fakeSession() } as never);
	});
	afterEach(() => {
		vi.restoreAllMocks();
		ssoState.reset();
		if (stepUpState.open) stepUpState.finish(false);
		sessionState.current = undefined;
	});

	it('links an identity at the provider', async () => {
		vi.spyOn(api.auth, 'listOidcIdentities').mockResolvedValue(
			create(ListOidcIdentitiesResponseSchema)
		);
		const begin = vi
			.spyOn(api.auth, 'beginOidc')
			.mockResolvedValue({ authorizationUrl: 'https://id.example/auth' } as never);
		const leave = vi.spyOn(browser, 'leave').mockImplementation(() => {});
		render(SsoIdentities);
		await expect.element(page.getByText('No identity linked yet.')).toBeVisible();
		await page.getByRole('button', { name: 'Link with Keycloak' }).click();
		await vi.waitFor(() => expect(leave).toHaveBeenCalledWith('https://id.example/auth'));
		expect(begin.mock.calls[0][0]).toEqual({ intent: 2, next: '/settings/security' });
	});

	it('asks for the step-up before linking and stops when it is cancelled', async () => {
		vi.spyOn(api.auth, 'listOidcIdentities').mockResolvedValue(
			create(ListOidcIdentitiesResponseSchema)
		);
		vi.spyOn(api.auth, 'beginOidc').mockRejectedValue(stepUpError());
		const leave = vi.spyOn(browser, 'leave').mockImplementation(() => {});
		render(SsoIdentities);
		await page.getByRole('button', { name: 'Link with Keycloak' }).click();
		await vi.waitFor(() => expect(stepUpState.open).toBe(true));
		stepUpState.finish(false);
		await expect.element(page.getByRole('button', { name: 'Link with Keycloak' })).toBeEnabled();
		expect(leave).not.toHaveBeenCalled();
		expect(page.getByRole('alert').elements()).toHaveLength(0);
	});

	it('lists identities as text and unlinks while a password remains', async () => {
		vi.spyOn(api.auth, 'listOidcIdentities')
			.mockResolvedValueOnce(linked)
			.mockResolvedValue(create(ListOidcIdentitiesResponseSchema));
		const unlink = vi.spyOn(api.auth, 'unlinkOidcIdentity').mockResolvedValue({} as never);
		render(SsoIdentities, { taken: true });
		await expect.element(page.getByText(hostile)).toBeVisible();
		expect(document.querySelector('img')).toBeNull();
		await expect.element(page.getByText(/linked to another account already/)).toBeVisible();
		await page.getByRole('button', { name: 'Unlink' }).click();
		await page.getByRole('button', { name: 'Really unlink' }).click();
		await vi.waitFor(() =>
			expect(unlink).toHaveBeenCalledWith({ issuer: 'https://id.example/realms/ops' })
		);
		await expect.element(page.getByText('No identity linked yet.')).toBeVisible();
	});

	it("won't unlink the only way in", async () => {
		sessionState.current = { ...fakeSession(), hasPassword: false, oidcLinked: true };
		vi.spyOn(api.auth, 'listOidcIdentities').mockResolvedValue(linked);
		render(SsoIdentities);
		await expect.element(page.getByRole('button', { name: 'Unlink' })).toBeDisabled();
		await expect.element(page.getByText(/only way to sign in/)).toBeVisible();
	});

	it('stays away without a provider and identities', async () => {
		vi.spyOn(api.auth, 'getLoginOptions').mockResolvedValue({ oidc: false, oidcName: '' } as never);
		const list = vi
			.spyOn(api.auth, 'listOidcIdentities')
			.mockResolvedValue(create(ListOidcIdentitiesResponseSchema));
		render(SsoIdentities);
		await vi.waitFor(() => expect(list).toHaveBeenCalledOnce());
		await new Promise((resolve) => setTimeout(resolve, 50));
		expect(page.getByText('Sign-in over SSO').elements()).toHaveLength(0);
	});

	it('says so when the identities fail to load', async () => {
		vi.spyOn(api.auth, 'listOidcIdentities').mockRejectedValue(rpcError(Code.Unavailable));
		render(SsoIdentities);
		await expect.element(page.getByRole('alert')).toBeVisible();
	});
});
