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

/** Runs a shell command as root in a service; throws with the whole output if it fails. */
export function exec(service: string, command: string, input?: string): string {
	const res = spawnSync(
		'docker',
		['compose', '-f', composeFile, 'exec', '-T', service, 'sh', '-c', command],
		{
			encoding: 'utf8',
			input,
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
