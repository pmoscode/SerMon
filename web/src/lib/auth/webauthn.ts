/**
 * The browser side of passkeys (WebAuthn, M3-24): the server's options (JSON) into
 * navigator.credentials and the answer back as JSON. Converted by hand, so browsers without
 * PublicKeyCredential.parse…FromJSON work too.
 */

/** Why a ceremony in the browser failed. */
export type CeremonyFailure = 'cancelled' | 'unsupported' | 'failed';

export class CeremonyError extends Error {
	constructor(readonly reason: CeremonyFailure) {
		super(reason);
	}
}

/** Whether the browser can use passkeys at all. */
export function supported(): boolean {
	return (
		typeof window !== 'undefined' && 'PublicKeyCredential' in window && !!navigator.credentials
	);
}

/** The browser's conditional UI (passkeys offered in the autofill of the e-mail field). */
export async function conditionalAvailable(): Promise<boolean> {
	if (!supported()) return false;
	const pkc = window.PublicKeyCredential as unknown as {
		isConditionalMediationAvailable?: () => Promise<boolean>;
	};
	try {
		return (await pkc.isConditionalMediationAvailable?.()) ?? false;
	} catch {
		return false;
	}
}

export function fromBase64url(s: string): ArrayBuffer {
	// atob decodes without padding (forgiving base64).
	const bin = atob(s.replace(/-/g, '+').replace(/_/g, '/'));
	const out = new Uint8Array(bin.length);
	for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
	return out.buffer;
}

export function toBase64url(buf: ArrayBuffer | null | undefined): string {
	if (!buf) return '';
	const bytes = new Uint8Array(buf);
	let bin = '';
	for (const b of bytes) bin += String.fromCharCode(b);
	return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

type Descriptor = { id: string; type: string; transports?: string[] };

function descriptors(list: Descriptor[] | undefined): PublicKeyCredentialDescriptor[] | undefined {
	return list?.map((d) => ({
		type: 'public-key',
		id: fromBase64url(d.id),
		transports: d.transports as AuthenticatorTransport[] | undefined
	}));
}

/** The creation options of the server ({"publicKey": …}) for navigator.credentials.create. */
export function creationOptions(json: string): PublicKeyCredentialCreationOptions {
	const { publicKey: o } = JSON.parse(json) as {
		publicKey: Record<string, unknown> & {
			challenge: string;
			user: { id: string; name: string; displayName: string };
			excludeCredentials?: Descriptor[];
		};
	};
	return {
		...(o as unknown as PublicKeyCredentialCreationOptions),
		challenge: fromBase64url(o.challenge),
		user: { ...o.user, id: fromBase64url(o.user.id) },
		excludeCredentials: descriptors(o.excludeCredentials)
	};
}

/** The request options of the server ({"publicKey": …}) for navigator.credentials.get. */
export function requestOptions(json: string): PublicKeyCredentialRequestOptions {
	const { publicKey: o } = JSON.parse(json) as {
		publicKey: Record<string, unknown> & { challenge: string; allowCredentials?: Descriptor[] };
	};
	return {
		...(o as unknown as PublicKeyCredentialRequestOptions),
		challenge: fromBase64url(o.challenge),
		allowCredentials: descriptors(o.allowCredentials)
	};
}

function failure(error: unknown): CeremonyError {
	const name = (error as { name?: string })?.name;
	if (name === 'NotAllowedError' || name === 'AbortError') return new CeremonyError('cancelled');
	if (name === 'NotSupportedError' || name === 'SecurityError')
		return new CeremonyError('unsupported');
	return new CeremonyError('failed');
}

/** Creates a passkey; the answer as JSON for the server. */
export async function create(optionsJson: string): Promise<string> {
	if (!supported()) throw new CeremonyError('unsupported');
	let cred: PublicKeyCredential;
	try {
		cred = (await navigator.credentials.create({
			publicKey: creationOptions(optionsJson)
		})) as PublicKeyCredential;
	} catch (error) {
		throw failure(error);
	}
	if (!cred) throw new CeremonyError('cancelled');
	const r = cred.response as AuthenticatorAttestationResponse;
	return JSON.stringify({
		id: cred.id,
		rawId: toBase64url(cred.rawId),
		type: cred.type,
		authenticatorAttachment: cred.authenticatorAttachment ?? undefined,
		clientExtensionResults: cred.getClientExtensionResults(),
		response: {
			clientDataJSON: toBase64url(r.clientDataJSON),
			attestationObject: toBase64url(r.attestationObject),
			transports: r.getTransports?.() ?? []
		}
	});
}

/** Asserts with a passkey; the answer as JSON for the server. */
export async function get(
	optionsJson: string,
	opts: { conditional?: boolean; signal?: AbortSignal } = {}
): Promise<string> {
	if (!supported()) throw new CeremonyError('unsupported');
	let cred: PublicKeyCredential;
	try {
		cred = (await navigator.credentials.get({
			publicKey: requestOptions(optionsJson),
			mediation: opts.conditional ? ('conditional' as CredentialMediationRequirement) : undefined,
			signal: opts.signal
		})) as PublicKeyCredential;
	} catch (error) {
		throw failure(error);
	}
	if (!cred) throw new CeremonyError('cancelled');
	const r = cred.response as AuthenticatorAssertionResponse;
	return JSON.stringify({
		id: cred.id,
		rawId: toBase64url(cred.rawId),
		type: cred.type,
		authenticatorAttachment: cred.authenticatorAttachment ?? undefined,
		clientExtensionResults: cred.getClientExtensionResults(),
		response: {
			clientDataJSON: toBase64url(r.clientDataJSON),
			authenticatorData: toBase64url(r.authenticatorData),
			signature: toBase64url(r.signature),
			userHandle: toBase64url(r.userHandle)
		}
	});
}
