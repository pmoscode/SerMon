import { LogSeverity } from '#lib/api/gen/sermon/web/v1/log_settings_pb.js';

/** The histogram shows four groups of severities. */
export const groups = ['error', 'warning', 'info', 'debug'] as const;
export type Group = (typeof groups)[number];

export function groupOf(severity: LogSeverity): Group {
	if (severity >= LogSeverity.EMERGENCY && severity <= LogSeverity.ERROR) return 'error';
	if (severity === LogSeverity.WARNING) return 'warning';
	if (severity === LogSeverity.NOTICE || severity === LogSeverity.INFO) return 'info';
	return 'debug';
}

/** Counts of one severity: at is the end of a step in ms. */
export interface CountSeries {
	severity: LogSeverity;
	points: { at: number; count: number }[];
}

export interface Bucket {
	start: number;
	end: number;
	counts: Record<Group, number>;
	total: number;
}

/** Bounds the number of bars, also for a step that doesn't fit the range. */
const MAX_BUCKETS = 1000;

/**
 * The bars of the range. A count at t covers the step (t − step, t]; counts before the range are
 * left out, later ones go into the last bar.
 */
export function buckets(
	series: CountSeries[],
	step: number,
	range: { from: number; to: number }
): Bucket[] {
	if (!(step > 0) || range.to <= range.from) return [];
	const width = Math.max(step, (range.to - range.from) / MAX_BUCKETS);
	const n = Math.ceil((range.to - range.from) / width);
	const out: Bucket[] = Array.from({ length: n }, (_, i) => ({
		start: range.from + i * width,
		end: Math.min(range.from + (i + 1) * width, range.to),
		counts: { error: 0, warning: 0, info: 0, debug: 0 },
		total: 0
	}));
	for (const s of series) {
		const group = groupOf(s.severity);
		for (const p of s.points) {
			// A point counts the lines of the step that ends at it, so it belongs to the bucket that
			// holds its time (end included): points aligned to the range or to the step (the counts of
			// the ingest, V1-24) land the same; the tolerance absorbs rounding of the division.
			const i = Math.ceil((p.at - range.from) / width - 1e-9) - 1;
			if (i < 0 || !Number.isFinite(p.count)) continue;
			const bucket = out[Math.min(i, n - 1)];
			bucket.counts[group] += p.count;
			bucket.total += p.count;
		}
	}
	return out;
}
