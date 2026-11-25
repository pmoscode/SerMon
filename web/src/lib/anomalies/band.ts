/**
 * The anomaly band of the charts (KONZEPT §13, M4-25): which stretches of a chart's series were
 * anomalous. Pure, for the charts and their tests.
 */

/** A stretch in milliseconds, from inclusive, to exclusive. */
export interface Band {
	from: number;
	to: number;
}

/** What the band needs of a series of GetNodeAnomalies. */
export interface AnomalousSeries {
	metric: string;
	labels: Record<string, string>;
	intervals: Band[];
}

/** What the band needs of a chart's query. */
export interface BandQuery {
	metric: string;
	labels?: Record<string, string>;
	skip?: (labels: Record<string, string>) => boolean;
}

const DAY = 86_400_000;
/** The results reach 30 days back; one request covers at most 7 (ADR 0037). */
export const MAX_RANGE = 7 * DAY;
export const RETENTION = 30 * DAY;

/** The part of the range the band can show: its last 7 days within the last 30; undefined if none. */
export function bandRange(range: Band, now: number): Band | undefined {
	const from = Math.max(range.from, range.to - MAX_RANGE, now - RETENTION + 60_000);
	return from < range.to ? { from, to: range.to } : undefined;
}

/** Sorts and merges stretches that touch or overlap. */
export function merge(bands: Band[]): Band[] {
	const sorted = [...bands].sort((a, b) => a.from - b.from);
	const out: Band[] = [];
	for (const b of sorted) {
		const last = out.at(-1);
		if (last && b.from <= last.to) last.to = Math.max(last.to, b.to);
		else out.push({ ...b });
	}
	return out;
}

function matches(s: AnomalousSeries, q: BandQuery): boolean {
	if (s.metric !== q.metric || q.skip?.(s.labels)) return false;
	return Object.entries(q.labels ?? {}).every(([k, v]) => s.labels[k] === v);
}

/** The band of a chart: the anomalous stretches of the series its queries show, merged. */
export function bandsFor(series: AnomalousSeries[], queries: BandQuery[]): Band[] {
	return merge(
		series.filter((s) => queries.some((q) => matches(s, q))).flatMap((s) => s.intervals)
	);
}
