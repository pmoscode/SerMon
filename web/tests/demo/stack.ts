import { execFileSync, spawn, spawnSync } from 'node:child_process';
import { X509Certificate } from 'node:crypto';
import { appendFileSync, readdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join, resolve } from 'node:path';

/**
 * Access to the containers of the production compose with the E2E overlay (scripts/e2e-demo.sh,
 * M2-26). The script sets COMPOSE_FILE and COMPOSE_ENV_FILES; the defaults are for a run by hand
 * from web/ against an environment left running with KEEP=1.
 */
const e2eDir = resolve(process.env.E2E_DIR ?? '../deploy/e2e');
const composeEnv = {
	...process.env,
	COMPOSE_FILE:
		process.env.COMPOSE_FILE ??
		`${join(e2eDir, '..', 'compose', 'compose.yaml')}:${join(e2eDir, 'compose.yaml')}`,
	COMPOSE_ENV_FILES: process.env.COMPOSE_ENV_FILES ?? join(e2eDir, 'e2e.env')
};

export function compose(args: string[], input?: string): string {
	return execFileSync('docker', ['compose', ...args], {
		encoding: 'utf8',
		env: composeEnv,
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
		['compose', 'exec', '-T', ...names, service, 'sh', '-c', command],
		{
			encoding: 'utf8',
			input,
			env: { ...composeEnv, ...env },
			maxBuffer: 16 * 1024 * 1024
		}
	);
	const output = `${res.stdout ?? ''}${res.stderr ?? ''}`;
	if (res.status !== 0) throw new Error(`${service}: exit ${res.status}\n$ ${command}\n${output}`);
	return output;
}

/**
 * Like exec, but in the background: resolves with the exit status and the whole output, so the
 * test can go on while a long command (the budget measurement) runs.
 */
export function execAsync(
	service: string,
	command: string,
	input?: string
): Promise<{ status: number | null; output: string }> {
	return new Promise((resolve, reject) => {
		const child = spawn('docker', ['compose', 'exec', '-T', service, 'sh', '-c', command], {
			env: composeEnv
		});
		let output = '';
		child.stdout.on('data', (chunk: Buffer) => (output += chunk.toString()));
		child.stderr.on('data', (chunk: Buffer) => (output += chunk.toString()));
		child.on('error', reject);
		child.on('close', (status) => resolve({ status, output }));
		child.stdin.end(input ?? '');
	});
}

/** A script of scripts/ in the repository, to feed to a host over stdin. */
export function repoScript(name: string): string {
	return readFileSync(join(e2eDir, '..', '..', 'scripts', name), 'utf8');
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
	const dir = join(e2eDir, '.build', 'downloads');
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
