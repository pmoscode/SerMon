/** Fakes of navigator.credentials answers for component tests (M3-24). */
const buf = (...b: number[]) => new Uint8Array(b).buffer;

export function fakeAttestation(): Credential {
	return {
		id: 'AQI',
		rawId: buf(1, 2),
		type: 'public-key',
		authenticatorAttachment: 'platform',
		getClientExtensionResults: () => ({}),
		response: {
			clientDataJSON: buf(3),
			attestationObject: buf(4),
			getTransports: () => ['internal']
		}
	} as unknown as Credential;
}

export function fakeAssertion(): Credential {
	return {
		id: 'AQI',
		rawId: buf(1, 2),
		type: 'public-key',
		authenticatorAttachment: 'platform',
		getClientExtensionResults: () => ({}),
		response: {
			clientDataJSON: buf(3),
			authenticatorData: buf(5),
			signature: buf(6),
			userHandle: buf(7)
		}
	} as unknown as Credential;
}

export const creationJson = JSON.stringify({
	publicKey: {
		challenge: 'AQID',
		rp: { id: 'localhost', name: 'SerMon' },
		user: { id: 'BAU', name: 'mia@example.org', displayName: 'Mia' },
		pubKeyCredParams: [{ type: 'public-key', alg: -7 }]
	}
});

export const requestJson = JSON.stringify({ publicKey: { challenge: 'CAk', rpId: 'localhost' } });

/** The browser's refusal when the user cancels. */
export function notAllowed(): DOMException {
	return new DOMException('The operation either timed out or was not allowed.', 'NotAllowedError');
}
