import { afterEach, describe, expect, it, vi } from 'vitest';
import { api, Code } from '#lib/api/client.js';
import { fakeSession, rpcError } from '#lib/test/fakes.js';
import { sessionState } from './session.svelte.js';

describe('sessionState', () => {
	afterEach(() => {
		vi.restoreAllMocks();
		sessionState.status = 'unknown';
		sessionState.current = undefined;
	});

	it('loads the cookie session', async () => {
		vi.spyOn(api.auth, 'getSession').mockResolvedValue({
			session: fakeSession('Alpha', 'Beta')
		} as never);

		expect(await sessionState.load()).toBe('authenticated');
		expect(sessionState.organization?.name).toBe('Alpha');
	});

	it('is anonymous without a session but fails when the server is down', async () => {
		const get = vi.spyOn(api.auth, 'getSession').mockRejectedValue(rpcError(Code.Unauthenticated));
		expect(await sessionState.load()).toBe('anonymous');

		get.mockRejectedValue(rpcError(Code.Unavailable));
		await expect(sessionState.load()).rejects.toThrow();
	});

	it('expires when a call of the app is UNAUTHENTICATED, not on a failed login', async () => {
		vi.spyOn(api.auth, 'getSession').mockResolvedValue({ session: fakeSession() } as never);
		await sessionState.load();
		vi.spyOn(globalThis, 'fetch').mockImplementation(
			async () =>
				new Response(JSON.stringify({ code: 'unauthenticated', message: 'session ended' }), {
					status: 401,
					headers: { 'Content-Type': 'application/json' }
				})
		);

		await expect(api.auth.login({ email: 'a@b.c', password: 'x' })).rejects.toThrow();
		expect(sessionState.status).toBe('authenticated');

		await expect(api.fleet.getFleetSummary({})).rejects.toThrow();
		expect(sessionState.status).toBe('expired');
		expect(sessionState.current).toBeUndefined();
	});

	it('stays anonymous-after-expiry until the next login', async () => {
		sessionState.expire();
		vi.spyOn(api.auth, 'getSession').mockRejectedValue(rpcError(Code.Unauthenticated));

		expect(await sessionState.load()).toBe('expired');
	});

	it('switches the organization with the new session', async () => {
		vi.spyOn(api.auth, 'getSession').mockResolvedValue({
			session: fakeSession('Alpha', 'Beta')
		} as never);
		await sessionState.load();
		const beta = fakeSession('Alpha', 'Beta');
		beta.activeOrganizationId = beta.organizations[1].id;
		const sw = vi
			.spyOn(api.auth, 'switchOrganization')
			.mockResolvedValue({ session: beta } as never);

		await sessionState.switchOrganization(beta.organizations[1].id);

		expect(sw.mock.calls[0][0]).toEqual({ organizationId: beta.organizations[1].id });
		expect(sessionState.organization?.name).toBe('Beta');
	});

	it('ends the session locally even when logout fails', async () => {
		vi.spyOn(api.auth, 'getSession').mockResolvedValue({ session: fakeSession() } as never);
		await sessionState.load();
		vi.spyOn(api.auth, 'logout').mockRejectedValue(rpcError(Code.Unavailable));

		await expect(sessionState.logout()).rejects.toThrow();
		expect(sessionState.status).toBe('anonymous');
	});
});
