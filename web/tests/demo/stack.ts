import { type APIRequestContext, type Page, expect } from '@playwright/test';
import { execFileSync, spawn, spawnSync } from 'node:child_process';
import { X509Certificate, createHash } from 'node:crypto';
import { appendFileSync, mkdirSync, readdirSync, readFileSync, writeFileSync } from 'node:fs';
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

/**
 * Puts a beta release of the agent into the downloads (ADR 0036), signed with the run's release
 * key for VERSION: BINARY or, without, the agent binary of this run. Returns its SHA-256.
 */
export function publishBeta(version: string, binary?: Buffer): string {
	const dir = join(e2eDir, '.build', 'downloads');
	const name = readdirSync(dir).find((f) => /^sermon-agent-linux-\w+$/.test(f));
	if (!name) throw new Error(`no agent binary in ${dir}`);
	mkdirSync(join(dir, 'beta'), { recursive: true });
	const path = join(dir, 'beta', name);
	writeFileSync(path, binary ?? readFileSync(join(dir, name)), { mode: 0o755 });
	execFileSync(join(e2eDir, '..', '..', 'scripts', 'release-sign.sh'), [
		'sign',
		join(e2eDir, '.build', 'release.pem'),
		'--version',
		version,
		path
	]);
	return createHash('sha256').update(readFileSync(path)).digest('hex');
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

// Test fixtures for a throwaway installation, not secrets.
export const PASSWORD = 'correct horse battery staple'; // betterleaks:allow
export const admin = { name: 'Ann Admin', email: 'ann@example.org' };

export async function login(page: Page, email: string) {
	await page.goto('/login');
	await page.getByLabel('Email address').fill(email);
	await page.getByLabel('Password', { exact: true }).fill(PASSWORD);
	await page.getByRole('button', { name: 'Sign in', exact: true }).click();
	await expect(page).toHaveURL('/fleet');
}

/** A Connect call with the cookies of the page (the server checks the origin). */
export async function call<T>(
	request: APIRequestContext,
	baseURL: string,
	method: string,
	body: object
): Promise<T> {
	const res = await request.post(`/sermon.web.v1.${method}`, {
		data: body,
		headers: { Origin: new URL(baseURL).origin, 'Content-Type': 'application/json' }
	});
	expect(res.ok(), `${method}: ${res.status()} ${await res.text()}`).toBe(true);
	return (await res.json()) as T;
}

/** The messages ntfy keeps for a topic (published on 127.0.0.1 by the overlay, M2-27). */
export async function ntfyMessages(
	topic: string
): Promise<{ title?: string; message?: string; tags?: string[] }[]> {
	const port = process.env.SERMON_E2E_NTFY_PORT ?? '18090';
	const res = await fetch(`http://127.0.0.1:${port}/${topic}/json?poll=1&since=all`);
	const text = await res.text();
	return text
		.split('\n')
		.filter((line) => line.trim() !== '')
		.map(
			(line) =>
				JSON.parse(line) as { event: string; title?: string; message?: string; tags?: string[] }
		)
		.filter((m) => m.event === 'message');
}

/** The bodies the Slack mock received at a webhook path (deploy/e2e/slack-mock, M2-27). */
export async function slackMessages(path: string): Promise<{ text: string; raw: string }[]> {
	const port = process.env.SERMON_E2E_SLACK_PORT ?? '18026';
	const messages = (await (await fetch(`http://127.0.0.1:${port}/messages`)).json()) as {
		path: string;
		body: { text: string };
	}[];
	return messages
		.filter((m) => m.path === path)
		.map((m) => ({ text: m.body.text, raw: JSON.stringify(m.body) }));
}

/** Runs SQL as the superuser in the database container (only to age timers in the demo). */
export function ownerSQL(sql: string): string {
	return exec('postgres', 'psql -v ON_ERROR_STOP=1 -qtA -U postgres -d sermon', sql);
}

/** What a live view of a node's processes delivered (M3-14). */
export interface LiveViewResult {
	status: number;
	snapshots: number;
	offline: number;
	refused: string;
	/** The end of the stream, if the server ended it (Connect end-stream message). */
	end: string;
	/** The processes of the last snapshot. */
	processes: { pid: number; name?: string; user?: string; cmdline?: string; unit?: string }[];
}

/**
 * Watches the node's processes for some seconds in the browser of `page` (its session cookie,
 * its origin), over the Connect streaming protocol with JSON – as the UI will (M3-15).
 */
export async function watchProcesses(
	page: Page,
	nodeId: string,
	seconds: number
): Promise<LiveViewResult> {
	return page.evaluate(
		async ({ nodeId, seconds }) => {
			const body = new TextEncoder().encode(JSON.stringify({ nodeId }));
			const envelope = new Uint8Array(5 + body.length);
			new DataView(envelope.buffer).setUint32(1, body.length);
			envelope.set(body, 5);
			const abort = new AbortController();
			const timer = setTimeout(() => abort.abort(), seconds * 1000);
			const out = {
				status: 0,
				snapshots: 0,
				offline: 0,
				refused: '',
				end: '',
				processes: [] as LiveViewResult['processes']
			};
			try {
				const res = await fetch('/sermon.web.v1.LiveViewService/WatchProcesses', {
					method: 'POST',
					headers: {
						'Content-Type': 'application/connect+json',
						'Connect-Protocol-Version': '1'
					},
					body: envelope,
					signal: abort.signal
				});
				out.status = res.status;
				const reader = res.body!.getReader();
				let buffer = new Uint8Array(0);
				for (;;) {
					const { value, done } = await reader.read();
					if (done) break;
					const joined = new Uint8Array(buffer.length + value.length);
					joined.set(buffer);
					joined.set(value, buffer.length);
					buffer = joined;
					while (buffer.length >= 5) {
						const length = new DataView(buffer.buffer, buffer.byteOffset).getUint32(1);
						if (buffer.length < 5 + length) break;
						const flags = buffer[0];
						const text = new TextDecoder().decode(buffer.subarray(5, 5 + length));
						buffer = buffer.slice(5 + length);
						if (flags & 2) {
							out.end = text;
							continue;
						}
						const msg = JSON.parse(text);
						if (msg.snapshot) {
							out.snapshots++;
							out.processes = msg.snapshot.processes ?? [];
						} else if (msg.offline) {
							out.offline++;
						} else if (msg.refused) {
							out.refused = msg.refused;
						}
					}
				}
			} catch (error) {
				if (!abort.signal.aborted) throw error;
			} finally {
				clearTimeout(timer);
			}
			return out;
		},
		{ nodeId, seconds }
	);
}
