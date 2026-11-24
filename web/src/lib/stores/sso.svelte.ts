import { api, Code, codeOf } from '#lib/api/client.js';
import { OidcIntent } from '#lib/api/gen/sermon/web/v1/auth_pb.js';
import { safeRedirect } from '#lib/navigation/redirect.js';
import { withStepUp } from '#lib/stores/stepup.svelte.js';

/** Why a sign-in at the provider could not start. */
export type SsoFailure = 'rate_limited' | 'unavailable' | 'step_up' | 'signed_out';

/** Leaves the app for the provider's login page; tests replace it. */
export const browser = {
	leave(url: string): void {
		window.location.assign(url);
	}
};

/**
 * The single sign-on of the instance (M3-20, M3-22): whether there is a provider and its name.
 * Loaded once; the login page, the step-up dialog and the settings read it.
 */
class SsoState {
	enabled = $state(false);
	name = $state('');
	/** The server offers passkeys (M3-24). */
	passkeys = $state(false);
	#loading: Promise<void> | undefined;

	/** Asks the server once; failures leave SSO off. */
	load(): Promise<void> {
		this.#loading ??= api.auth
			.getLoginOptions({})
			.then((res) => {
				this.enabled = res.oidc;
				this.name = res.oidcName;
				this.passkeys = res.passkeys;
			})
			.catch(() => {
				this.#loading = undefined;
			});
		return this.#loading;
	}

	/**
	 * Starts a sign-in, link or step-up at the provider and leaves for its login page; it returns
	 * to next (a path of the app). Returns the reason if it could not start.
	 */
	async start(intent: OidcIntent, next: string): Promise<SsoFailure | undefined> {
		try {
			// Linking asks for a step-up first; the dialog runs it and the call once more.
			const res = await withStepUp(() => api.auth.beginOidc({ intent, next: safeRedirect(next) }));
			browser.leave(res.authorizationUrl);
			return undefined;
		} catch (error) {
			switch (codeOf(error)) {
				case Code.ResourceExhausted:
					return 'rate_limited';
				case Code.FailedPrecondition:
					// The step-up was cancelled, or there is no provider.
					return 'step_up';
				case Code.Unauthenticated:
					return 'signed_out';
				default:
					return 'unavailable';
			}
		}
	}

	/** Test helper: forget what was loaded. */
	reset(): void {
		this.enabled = false;
		this.name = '';
		this.passkeys = false;
		this.#loading = undefined;
	}
}

export const ssoState = new SsoState();
export { OidcIntent };
