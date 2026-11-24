import { api, Code, codeOf } from '#lib/api/client.js';
import { PasskeyPurpose } from '#lib/api/gen/sermon/web/v1/auth_pb.js';
import { CeremonyError, get, type CeremonyFailure } from '#lib/auth/webauthn.js';
import { sessionState } from '#lib/stores/session.svelte.js';

/**
 * Assertions with a passkey (M3-24): the sign-in without a password, the second factor after it
 * and the step-up. The session of the answer replaces the current one.
 */

/** Why an assertion failed; refused: the server didn't accept the passkey. */
export type PasskeyFailure = CeremonyFailure | 'refused' | 'none' | 'rate_limited' | 'unavailable';

export { PasskeyPurpose };

/**
 * Runs an assertion of the purpose; conditional waits for the autofill of the browser until
 * signal aborts it.
 */
export async function assertPasskey(
	purpose: PasskeyPurpose,
	opts: { conditional?: boolean; signal?: AbortSignal } = {}
): Promise<PasskeyFailure | undefined> {
	try {
		const begun = await api.auth.beginPasskey({ purpose });
		const credentialJson = await get(begun.optionsJson, opts);
		const res = await api.auth.finishPasskey({
			purpose,
			ceremonyId: begun.ceremonyId,
			credentialJson
		});
		sessionState.current = res.session;
		sessionState.status = 'authenticated';
		return undefined;
	} catch (error) {
		if (error instanceof CeremonyError) return error.reason;
		switch (codeOf(error)) {
			case Code.Unauthenticated:
			case Code.PermissionDenied:
				return 'refused';
			case Code.NotFound:
				return 'none';
			case Code.ResourceExhausted:
				return 'rate_limited';
			default:
				return 'unavailable';
		}
	}
}
