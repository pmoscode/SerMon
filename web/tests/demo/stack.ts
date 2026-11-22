import { execFileSync, spawnSync } from 'node:child_process';
import { X509Certificate } from 'node:crypto';
import { appendFileSync, readdirSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';

/** Access to the containers of deploy/e2e/compose.yaml (scripts/e2e-demo.sh). */
const composeFile = process.env.E2E_COMPOSE_FILE ?? '../deploy/e2e/compose.yaml';

export function compose(args: string[], input?: string): string {
	return execFileSync('docker', ['compose', '-f', composeFile, ...args], {
		encoding: 'utf8',
		input,
		stdio: ['pipe', 'pipe', 'pipe'],
		maxBuffer: 16 * 1024 * 1024
	});
}

/**
 * Runs a shell command as root in a service, with `env` in its environment (not in the command, so
 * a token stays out of the host's process list inside the container); throws with the whole output
 * if it fails.
 */
export function exec(
	service: string,
	command: string,
	input?: string,
	env: Record<string, string> = {}
): string {
	const names = Object.keys(env).flatMap((name) => ['-e', name]);
	const res = spawnSync(
		'docker',
		['compose', '-f', composeFile, 'exec', '-T', ...names, service, 'sh', '-c', command],
		{
			encoding: 'utf8',
			input,
			env: { ...process.env, ...env },
			maxBuffer: 16 * 1024 * 1024
		}
	);
	const output = `${res.stdout ?? ''}${res.stderr ?? ''}`;
	if (res.status !== 0) throw new Error(`${service}: exit ${res.status}\n$ ${command}\n${output}`);
	return output;
}

/** The setup token the server logged on its last start (it replaces the one from `init`). */
export function setupToken(): string {
	const tokens = [
		...compose(['logs', '--no-color', 'server']).matchAll(/"setup_token":"([^"]+)"/g)
	];
	const token = tokens.at(-1)?.[1];
	if (!token) throw new Error('no setup token in the server log');
	return token;
}

/** Caddy's root certificate (PEM) and the SHA-256 fingerprint of its intermediate CA. */
export function caddyCA(): { root: string; intermediateFingerprint: string } {
	const dir = '/data/caddy/pki/authorities/local';
	const root = exec('caddy', `cat ${dir}/root.crt`);
	const intermediate = new X509Certificate(exec('caddy', `cat ${dir}/intermediate.crt`));
	return { root, intermediateFingerprint: `sha256:${intermediate.fingerprint256}` };
}

/**
 * Appends a byte to the agent binary the server offers (bind mount of .build/downloads) and
 * returns the function that restores it: install.sh must refuse the modified file.
 */
export function tamperDownload(): () => void {
	const dir = join(dirname(composeFile), '.build', 'downloads');
	const binary = readdirSync(dir).find((f) => /^sermon-agent-linux-\w+$/.test(f));
	if (!binary) throw new Error(`no agent binary in ${dir}`);
	const path = join(dir, binary);
	const original = readFileSync(path);
	appendFileSync(path, Buffer.from([0]));
	return () => writeFileSync(path, original);
}

/** Mailpit's API of the e2e environment (published on 127.0.0.1). */
const mailpit = `http://127.0.0.1:${process.env.SERMON_E2E_MAIL_PORT ?? '18025'}`;

/**
 * The invitation link mailed to `to`: in production the server never hands it out, only the
 * mailbox gets it (M1-39). Waits until the mail arrived.
 */
export async function invitationLink(to: string, timeoutMs = 15_000): Promise<string> {
	const deadline = Date.now() + timeoutMs;
	for (;;) {
		const search = await fetch(`${mailpit}/api/v1/search?query=${encodeURIComponent(`to:${to}`)}`);
		const found = (await search.json()) as { messages?: { ID: string }[] };
		const id = found.messages?.[0]?.ID;
		if (id) {
			const message = (await (await fetch(`${mailpit}/api/v1/message/${id}`)).json()) as {
				Text: string;
			};
			const link = /https:\/\/\S+\/invite#[\w-]+/.exec(message.Text)?.[0];
			if (!link) throw new Error(`invitation mail to ${to} without link:\n${message.Text}`);
			return link;
		}
		if (Date.now() > deadline) throw new Error(`no invitation mail to ${to}`);
		await new Promise((resolve) => setTimeout(resolve, 250));
	}
}
