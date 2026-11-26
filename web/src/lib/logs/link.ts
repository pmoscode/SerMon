/**
 * Links into the log explorer, without its filter module: charts and alert cards on pages that
 * don't show logs (M4-25, the JavaScript budget of the node page). The parameters are those of
 * filterParams (filter.ts).
 */

/** The longest range of a query. */
export const MAX_RANGE = 14 * 86400_000;

/**
 * The query of a link to the logs of a node in a range (e.g. a chart's), at most 14 days;
 * optionally only those of a systemd unit.
 */
export function logsQuery(node: string, from: number, to: number, unit?: string): string {
	const params = new URLSearchParams();
	params.set('from', new Date(Math.max(from, to - MAX_RANGE)).toISOString());
	params.set('to', new Date(to).toISOString());
	params.append('node', node);
	if (unit) params.append('unit', unit);
	return `?${params}`;
}

/** Around an alert's start: 15 minutes before and after, but not beyond now. */
export const ALERT_WINDOW = 15 * 60_000;

/**
 * The query of a link to the logs of a node around the start of an alert; an alert of a unit
 * (its dimension `unit`) shows that unit's lines, not the node's noise around them (M4-28).
 */
export function alertLogsQuery(node: string, since: number, now: number, unit?: string): string {
	return logsQuery(node, since - ALERT_WINDOW, Math.min(since + ALERT_WINDOW, now), unit);
}
