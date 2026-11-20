/**
 * The install command of the add-node wizard (ADR 0012). The token travels in an exported
 * variable, never as an argument: arguments are visible to every local user in the process list,
 * and the leading space keeps the export out of the bash history.
 */

/** Header with the SHA-256 (hex) of install.sh, sent by the server. */
export const SCRIPT_DIGEST_HEADER = 'X-Sermon-Script-SHA256';

const TOKEN_VARIABLE = 'SERMON_ENROLLMENT_TOKEN';
const SCRIPT_FILE = 'sermon-install.sh';

/** Values that go into the command unquoted; anything else is refused, never escaped. */
const safe = {
	origin: /^https?:\/\/[A-Za-z0-9.-]+(:\d+)?$|^https?:\/\/\[[0-9A-Fa-f:.]+\](:\d+)?$/,
	secret: /^[A-Za-z0-9_-]+$/,
	digest: /^[0-9a-f]{64}$/,
	fingerprint: /^sha256:([0-9A-F]{2}:){31}[0-9A-F]{2}$/
};

/**
 * The fingerprint in the form the agent prints and the command uses (sha256:AB:CD:…), from what an
 * admin pastes: with or without prefix and colons, also the output of `openssl x509 -fingerprint`.
 * undefined if it isn't a SHA-256 fingerprint.
 */
export function normalizeFingerprint(text: string): string | undefined {
	let value = text.trim();
	const equals = value.lastIndexOf('=');
	if (/^sha256 fingerprint=/i.test(value)) value = value.slice(equals + 1);
	else if (/^sha256:/i.test(value)) value = value.slice('sha256:'.length);
	const digits = value.replaceAll(':', '').toUpperCase();
	if (!/^[0-9A-F]{64}$/.test(digits)) return undefined;
	return 'sha256:' + digits.match(/../g)!.join(':');
}

export interface InstallOptions {
	origin: string;
	secret: string;
	/** For servers without a publicly trusted certificate: checksum of install.sh and the pin. */
	selfSigned?: { digest: string; fingerprint: string };
}

function check(name: keyof typeof safe, value: string): string {
	if (!safe[name].test(value)) throw new Error(`unsafe ${name} for the install command`);
	return value;
}

/** The lines to paste on the server, joined with newlines. */
export function installCommand({ origin, secret, selfSigned }: InstallOptions): string {
	const server = check('origin', origin);
	const sudo = `sudo --preserve-env=${TOKEN_VARIABLE}`;
	const lines = [` export ${TOKEN_VARIABLE}=${check('secret', secret)}`];
	if (!selfSigned) {
		lines.push(`curl -fsSL ${server}/install.sh | ${sudo} sh -s -- --server ${server}`);
	} else {
		const digest = check('digest', selfSigned.digest);
		const fingerprint = check('fingerprint', selfSigned.fingerprint);
		lines.push(
			`curl -fsSLk ${server}/install.sh -o ${SCRIPT_FILE}`,
			`echo '${digest}  ${SCRIPT_FILE}' | sha256sum -c - && ${sudo} sh ${SCRIPT_FILE} --server ${server} --ca-fingerprint ${fingerprint}`
		);
	}
	return lines.join('\n');
}
