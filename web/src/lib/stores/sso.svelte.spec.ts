import { afterEach, describe, expect, it, vi } from 'vitest';
import { api } from '#lib/api/client.js';
import { browser, OidcIntent, ssoState } from './sso.svelte.js';

describe('ssoState', () => {
	afterEach(() => {
		vi.restoreAllMocks();
		ssoState.reset();
	});

	// M3-28: the browser leaves only for the provider's https page.
	it('refuses to leave for other schemes', () => {
		for (const url of ['javascript:alert(1)', 'data:text/html,x', 'ftp://idp.example/']) {
			expect(() => browser.leave(url)).toThrow(/non-https/);
		}
	});

	it('reports a refused address as unavailable', async () => {
		vi.spyOn(api.auth, 'beginOidc').mockResolvedValue({
			authorizationUrl: 'javascript:alert(1)'
		} as never);
		expect(await ssoState.start(OidcIntent.LOGIN, '/')).toBe('unavailable');
	});
});
