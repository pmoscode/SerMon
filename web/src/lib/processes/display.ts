/**
 * How the fields of a process are shown. Every text comes from the host and is shown as text only,
 * with invisible and bidi characters made visible (as log lines, KONZEPT §10.4).
 */
import type { ProcessInfo } from '#lib/api/gen/sermon/web/v1/live_view_pb.js';
import { formatBytes, formatDuration, formatPercent } from '#lib/format/format.js';
import { displayText } from '#lib/logs/text.js';
import { m } from '#lib/paraglide/messages.js';
import type { Locale } from '#lib/paraglide/runtime.js';

/** The name as ps shows it: kernel threads in brackets. */
export function processName(p: ProcessInfo): string {
	const name = displayText(p.name);
	return p.kernelThread ? `[${name}]` : name;
}

/** The CPU share of one core; "–" before the second snapshot. */
export function cpuText(p: ProcessInfo, locale: Locale): string {
	return p.cpuRatio === undefined ? '–' : formatPercent(p.cpuRatio, locale);
}

export function memoryText(p: ProcessInfo, locale: Locale): string | undefined {
	return p.memoryRatio === undefined ? undefined : formatPercent(p.memoryRatio, locale);
}

export function rssText(p: ProcessInfo, locale: Locale): string | undefined {
	return p.rssBytes === undefined ? undefined : formatBytes(Number(p.rssBytes), locale);
}

export function runtimeText(p: ProcessInfo, locale: Locale): string | undefined {
	return p.runtimeSeconds === undefined
		? undefined
		: formatDuration(Number(p.runtimeSeconds), locale);
}

/** The user name, else the UID; undefined if neither was readable. */
export function userText(p: ProcessInfo): string | undefined {
	if (p.user) return displayText(p.user);
	return p.uid === undefined ? undefined : String(p.uid);
}

const STATES: Record<string, () => string> = {
	R: m.procs_state_R,
	S: m.procs_state_S,
	D: m.procs_state_D,
	Z: m.procs_state_Z,
	T: m.procs_state_T,
	t: m.procs_state_t,
	I: m.procs_state_I,
	X: m.procs_state_X
};

/** The state with its meaning: "S – sleeping". */
export function stateText(p: ProcessInfo): string {
	const state = displayText(p.state);
	const name = STATES[p.state];
	return name ? `${state} – ${name()}` : state || m.procs_na();
}
