import { describe, expect, it } from 'vitest';
import { creationOptions, fromBase64url, requestOptions, toBase64url } from './webauthn.js';

const bytes = (b: ArrayBuffer | BufferSource) => Array.from(new Uint8Array(b as ArrayBuffer));

describe('webauthn options', () => {
	it('converts base64url both ways, with and without padding', () => {
		for (const sample of [[], [0], [0xfb, 0xff], [1, 2, 3, 4, 5]]) {
			const buf = new Uint8Array(sample).buffer;
			expect(bytes(fromBase64url(toBase64url(buf)))).toEqual(sample);
		}
		expect(toBase64url(new Uint8Array([0xfb, 0xff]).buffer)).toBe('-_8');
		expect(toBase64url(null)).toBe('');
	});

	it('decodes challenge, user and excluded credentials of a registration', () => {
		const o = creationOptions(
			JSON.stringify({
				publicKey: {
					challenge: 'AQID',
					rp: { id: 'sermon.example.com', name: 'SerMon' },
					user: { id: 'BAU', name: 'mia@example.org', displayName: 'Mia' },
					pubKeyCredParams: [{ type: 'public-key', alg: -7 }],
					excludeCredentials: [{ type: 'public-key', id: 'Bgc', transports: ['usb'] }],
					attestation: 'none'
				}
			})
		);
		expect(bytes(o.challenge)).toEqual([1, 2, 3]);
		expect(bytes(o.user.id)).toEqual([4, 5]);
		expect(o.user.name).toBe('mia@example.org');
		expect(o.rp.id).toBe('sermon.example.com');
		expect(o.attestation).toBe('none');
		expect(bytes(o.excludeCredentials![0].id)).toEqual([6, 7]);
		expect(o.excludeCredentials![0].transports).toEqual(['usb']);
	});

	it('decodes challenge and allowed credentials of an assertion', () => {
		const o = requestOptions(
			JSON.stringify({
				publicKey: { challenge: 'CAk', rpId: 'sermon.example.com', userVerification: 'required' }
			})
		);
		expect(bytes(o.challenge)).toEqual([8, 9]);
		expect(o.allowCredentials).toBeUndefined();
		expect(o.userVerification).toBe('required');
		const allowed = requestOptions(
			JSON.stringify({
				publicKey: { challenge: 'CAk', allowCredentials: [{ type: 'public-key', id: 'Cg' }] }
			})
		);
		expect(bytes(allowed.allowCredentials![0].id)).toEqual([10]);
	});
});
