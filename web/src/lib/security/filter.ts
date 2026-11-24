/**
 * The filters of the security dashboard (KONZEPT §16.4) and their form in the URL, so a view can
 * be shared and a node's tab can link into it. Invalid values are dropped; the server checks
 * everything again.
 */
import { cutBytes } from '#lib/logs/filter.js';

/** Relative ranges; the events are kept for a year (ADR 0018). */
export const securityPresets = ['1h', '24h', '7d', '30d', '90d', '365d'] as const;
export type SecurityPreset = (typeof securityPresets)[number];

const DAY = 86400_000;
const presetMs: Record<SecurityPreset, number> = {
	'1h': 3600_000,
	'24h': DAY,
	'7d': 7 * DAY,
	'30d': 30 * DAY,
	'90d': 90 * DAY,
	'365d': 365 * DAY
};

/** Bounds of the server (SecurityEventFilter). */
export const MAX_RANGE = 366 * DAY;
export const MAX_NODES = 100;
export const MAX_USER_BYTES = 128;
export const MAX_NETWORK = 43;

/** The kinds the list can filter on, as in the API. */
export const eventKinds = [
	'ssh.login.failed',
	'ssh.login.invalid_user',
	'ssh.login.success',
	'ssh.preauth.disconnect',
	'sudo.auth.failed',
	'sudo.not_allowed',
	'sudo.command',
	'account.created',
	'account.deleted',
	'system.reboot',
	'system.oom_kill'
] as const;
export type EventKind = (typeof eventKinds)[number];

/** Failed logins as the overview counts them. */
export const failedKinds: EventKind[] = ['ssh.login.failed', 'ssh.login.invalid_user'];
export const sudoFailureKinds: EventKind[] = ['sudo.auth.failed', 'sudo.not_allowed'];

export interface SecurityFilter {
	/** The relative range, unless from and to are set (zoom, links). */
	preset: SecurityPreset;
	from?: number;
	to?: number;
	nodes: string[];
	kinds: EventKind[];
	/** An exact user name; undefined: everyone (the empty name is what an attacker tried). */
	user?: string;
	/** An address or a network in CIDR notation. */
	network: string;
	/** Only logins from a source new to the node. */
	newOnly: boolean;
}

export function emptySecurityFilter(): SecurityFilter {
	return { preset: '24h', nodes: [], kinds: [], network: '', newOnly: false };
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
/** Addresses and networks only: digits, hex, dots, colons and one prefix length. */
const NETWORK = /^[0-9a-f.:]+(\/\d{1,3})?$/i;

function time(raw: string | null): number | undefined {
	if (!raw) return undefined;
	const ms = Date.parse(raw);
	return Number.isFinite(ms) ? ms : undefined;
}

/** Whether a text looks like an address or a network (the server parses it). */
export function validNetwork(text: string): boolean {
	return text === '' || (text.length <= MAX_NETWORK && NETWORK.test(text));
}

/** Reads the filter of a URL. */
export function parseSecurityFilter(params: URLSearchParams): SecurityFilter {
	const f = emptySecurityFilter();
	const preset = params.get('range');
	if (preset && (securityPresets as readonly string[]).includes(preset))
		f.preset = preset as SecurityPreset;
	const from = time(params.get('from'));
	const to = time(params.get('to'));
	if (from !== undefined && to !== undefined && to > from) {
		f.from = from;
		f.to = to;
	}
	f.nodes = [...new Set(params.getAll('node').filter((n) => UUID.test(n)))].slice(0, MAX_NODES);
	f.kinds = [
		...new Set(
			params
				.getAll('kind')
				.filter((k): k is EventKind => (eventKinds as readonly string[]).includes(k))
		)
	];
	const user = params.get('user');
	if (user !== null) f.user = cutBytes(user, MAX_USER_BYTES);
	const network = (params.get('net') ?? '').trim();
	if (validNetwork(network)) f.network = network;
	f.newOnly = params.get('new') === '1';
	return f;
}

/** The URL form of a filter; defaults are left out. */
export function securityFilterParams(f: SecurityFilter): URLSearchParams {
	const params = new URLSearchParams();
	if (f.from !== undefined && f.to !== undefined) {
		params.set('from', new Date(f.from).toISOString());
		params.set('to', new Date(f.to).toISOString());
	} else if (f.preset !== '24h') {
		params.set('range', f.preset);
	}
	for (const n of f.nodes) params.append('node', n);
	for (const k of f.kinds) params.append('kind', k);
	if (f.user !== undefined) params.set('user', f.user);
	if (f.network) params.set('net', f.network);
	if (f.newOnly) params.set('new', '1');
	return params;
}

/** The range of a filter in ms, at most MAX_RANGE and never empty. */
export function securityRange(f: SecurityFilter, now: number): { from: number; to: number } {
	if (f.from !== undefined && f.to !== undefined) {
		const to = Math.max(f.to, f.from + 1000);
		return { from: Math.max(f.from, to - MAX_RANGE), to };
	}
	return { from: now - presetMs[f.preset], to: now };
}
