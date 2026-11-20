import { describe, expect, it } from 'vitest';
import { installCommand, normalizeFingerprint } from './install.js';

const origin = 'https://sermon.example.com';
const secret = 'sme_abc-DEF_123'; // betterleaks:allow – test fixture, not a token
const hex = 'ab'.repeat(32);
const canonical = 'sha256:' + Array(32).fill('AB').join(':');

describe('normalizeFingerprint', () => {
	it.each([
		canonical,
		canonical.toLowerCase(),
		hex,
		Array(32).fill('ab').join(':'),
		`SHA256 Fingerprint=${Array(32).fill('AB').join(':')}`,
		`  ${canonical}\n`
	])('accepts %s', (text) => {
		expect(normalizeFingerprint(text)).toBe(canonical);
	});

	it.each(['', 'sha256:AB', hex + 'ab', 'zz'.repeat(32), `${hex}; rm -rf /`, `sha1:${hex}`])(
		'rejects %s',
		(text) => {
			expect(normalizeFingerprint(text)).toBeUndefined();
		}
	);
});

describe('installCommand', () => {
	it('keeps the token out of the process list and the history', () => {
		const lines = installCommand({ origin, secret }).split('\n');

		expect(lines).toEqual([
			` export SERMON_ENROLLMENT_TOKEN=${secret}`,
			`curl -fsSL ${origin}/install.sh | sudo --preserve-env=SERMON_ENROLLMENT_TOKEN sh -s -- --server ${origin}`
		]);
	});

	// Without a publicly trusted certificate curl can't authenticate the server: the script is
	// checked against the checksum the browser got from the server, before sudo runs it.
	it('checks the script by its checksum for self-signed servers', () => {
		const lines = installCommand({
			origin,
			secret,
			selfSigned: { digest: hex, fingerprint: canonical }
		}).split('\n');

		expect(lines).toEqual([
			` export SERMON_ENROLLMENT_TOKEN=${secret}`,
			`curl -fsSLk ${origin}/install.sh -o sermon-install.sh`,
			`echo '${hex}  sermon-install.sh' | sha256sum -c - && sudo --preserve-env=SERMON_ENROLLMENT_TOKEN sh sermon-install.sh --server ${origin} --ca-fingerprint ${canonical}`
		]);
	});

	it('refuses values that would need shell quoting', () => {
		expect(() => installCommand({ origin, secret: 'sme_x; rm -rf /' })).toThrow();
		expect(() =>
			installCommand({
				origin,
				secret,
				selfSigned: { digest: "x' && evil '", fingerprint: canonical }
			})
		).toThrow();
		expect(() => installCommand({ origin: 'https://a.example/$(id)', secret })).toThrow();
	});
});
