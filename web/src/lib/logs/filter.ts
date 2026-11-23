/**
 * The filters of the log explorer (KONZEPT §10.4) and their form in the URL, so that a view can be
 * shared and every jump from a chart or an alert lands on a filtered list. Invalid values are
 * dropped; the server checks everything again.
 */

/** Relative ranges; the logs are kept for 14 days (KONZEPT D12). */
export const logPresets = ['15m', '1h', '6h', '24h', '7d', '14d'] as const;
export type LogPreset = (typeof logPresets)[number];

const presetMs: Record<LogPreset, number> = {
	'15m': 15 * 60_000,
	'1h': 3600_000,
	'6h': 6 * 3600_000,
	'24h': 24 * 3600_000,
	'7d': 7 * 86400_000,
	'14d': 14 * 86400_000
};

/** The longest range of a query. */
export const MAX_RANGE = 14 * 86400_000;
/** Bounds of the server (LogFilter). */
export const MAX_NODES = 100;
export const MAX_UNITS = 20;
export const MAX_TEXT_BYTES = 256;

/** Severities from the most to the least severe, as in the API (LogSeverity 1 … 8). */
export const severities = [
	'emergency',
	'alert',
	'critical',
	'error',
	'warning',
	'notice',
	'info',
	'debug'
] as const;
export type SeverityName = (typeof severities)[number];

export type SourceName = 'journal' | 'file';

export interface LogFilter {
	/** The relative range, unless from and to are set (zoom, links). */
	preset: LogPreset;
	from?: number;
	to?: number;
	nodes: string[];
	group?: string;
	sources: SourceName[];
	units: string[];
	/** This severity and the more severe ones. */
	severity?: SeverityName;
	text: string;
	ignoreCase: boolean;
}

export function emptyFilter(): LogFilter {
	return { preset: '1h', nodes: [], sources: [], units: [], text: '', ignoreCase: false };
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** Cuts text to at most max bytes of UTF-8 without breaking a character. */
export function cutBytes(text: string, max: number): string {
	const encoder = new TextEncoder();
	if (encoder.encode(text).length <= max) return text;
	let out = '';
	for (const char of text) {
		if (encoder.encode(out + char).length > max) break;
		out += char;
	}
	return out;
}

function time(raw: string | null): number | undefined {
	if (!raw) return undefined;
	const ms = Date.parse(raw);
	return Number.isFinite(ms) ? ms : undefined;
}

/** Reads the filter of a URL. */
export function parseFilter(params: URLSearchParams): LogFilter {
	const f = emptyFilter();
	const preset = params.get('range');
	if (preset && (logPresets as readonly string[]).includes(preset)) f.preset = preset as LogPreset;
	const from = time(params.get('from'));
	const to = time(params.get('to'));
	if (from !== undefined && to !== undefined && to > from) {
		f.from = from;
		f.to = to;
	}
	f.nodes = [...new Set(params.getAll('node').filter((n) => UUID.test(n)))].slice(0, MAX_NODES);
	const group = params.get('group');
	if (group && UUID.test(group)) f.group = group;
	f.sources = [
		...new Set(
			params.getAll('source').filter((s): s is SourceName => s === 'journal' || s === 'file')
		)
	];
	f.units = [
		...new Set(params.getAll('unit').filter((u) => u.length > 0 && u.length <= 256))
	].slice(0, MAX_UNITS);
	const severity = params.get('severity');
	if (severity && (severities as readonly string[]).includes(severity))
		f.severity = severity as SeverityName;
	f.text = cutBytes(params.get('q') ?? '', MAX_TEXT_BYTES);
	f.ignoreCase = params.get('icase') === '1';
	return f;
}

/** The URL form of a filter; defaults are left out. */
export function filterParams(f: LogFilter): URLSearchParams {
	const params = new URLSearchParams();
	if (f.from !== undefined && f.to !== undefined) {
		params.set('from', new Date(f.from).toISOString());
		params.set('to', new Date(f.to).toISOString());
	} else if (f.preset !== '1h') {
		params.set('range', f.preset);
	}
	for (const n of f.nodes) params.append('node', n);
	if (f.group) params.set('group', f.group);
	for (const s of f.sources) params.append('source', s);
	for (const u of f.units) params.append('unit', u);
	if (f.severity) params.set('severity', f.severity);
	if (f.text) params.set('q', f.text);
	if (f.ignoreCase) params.set('icase', '1');
	return params;
}

/** The range of a filter in ms, at most MAX_RANGE and never empty. */
export function rangeOf(f: LogFilter, now: number): { from: number; to: number } {
	if (f.from !== undefined && f.to !== undefined) {
		const to = Math.max(f.to, f.from + 1000);
		return { from: Math.max(f.from, to - MAX_RANGE), to };
	}
	return { from: now - presetMs[f.preset], to: now };
}

/** The query of a link to the logs of a node in a range (e.g. a chart's), at most 14 days. */
export function logsQuery(node: string, from: number, to: number): string {
	const f = { ...emptyFilter(), nodes: [node], from: Math.max(from, to - MAX_RANGE), to };
	return `?${filterParams(f)}`;
}

/** Around an alert's start: 15 minutes before and after, but not beyond now. */
export const ALERT_WINDOW = 15 * 60_000;

/** The query of a link to the logs of a node around the start of an alert. */
export function alertLogsQuery(node: string, since: number, now: number): string {
	return logsQuery(node, since - ALERT_WINDOW, Math.min(since + ALERT_WINDOW, now));
}
