/**
 * The retention of an organization (M5-30) as the admin types it: whole days, empty for the
 * instance's default. The server checks the same limits again.
 */

export interface RetentionLimits {
	logMin: number;
	logMax: number;
	eventMin: number;
	eventMax: number;
	/** The superadmin's cap of the organization; 0: none. */
	cap: number;
}

/** Whole days; '' is 0 (the default); anything else undefined. */
export function parseDays(text: string): number | undefined {
	const t = text.trim();
	if (t === '') return 0;
	if (!/^\d{1,4}$/.test(t)) return undefined;
	return Number(t);
}

/** The fields out of range: 0 is the default, set values within the limits and the cap. */
export function checkRetention(
	logs: number | undefined,
	events: number | undefined,
	l: RetentionLimits
): ('logs' | 'events')[] {
	const ok = (v: number | undefined, min: number, max: number) =>
		v === 0 || (v !== undefined && v >= min && v <= max && (l.cap === 0 || v <= l.cap));
	const out: ('logs' | 'events')[] = [];
	if (!ok(logs, l.logMin, l.logMax)) out.push('logs');
	if (!ok(events, l.eventMin, l.eventMax)) out.push('events');
	return out;
}
