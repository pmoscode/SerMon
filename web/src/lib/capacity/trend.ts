/**
 * The trends of the capacity dashboard (KONZEPT §16, 6; M4-26): what a node used in the last
 * week of 30 days and how much more than in the first. Pure, for the page and its tests.
 */

/** Daily averages (0..1), oldest first. */
export type Days = number[];

/** What a trend says: the mean of the last 7 days and its change against the first 7. */
export interface Trend {
	recent: number;
	/** recent minus the mean of the first 7 days; undefined with less than 14 days. */
	change?: number;
}

const WEEK = 7;

function mean(values: number[]): number {
	return values.reduce((a, b) => a + b, 0) / values.length;
}

/** The trend of daily values; undefined without any. */
export function trendOf(days: Days): Trend | undefined {
	if (days.length === 0) return undefined;
	const recent = mean(days.slice(-WEEK));
	if (days.length < 2 * WEEK) return { recent };
	return { recent, change: recent - mean(days.slice(0, WEEK)) };
}

export type TrendKey = 'cpu' | 'memory';

/** A node with its two trends. */
export interface NodeTrends {
	nodeId: string;
	hostname: string;
	cpu?: Trend;
	memory?: Trend;
}

/** The top consumers: highest recent use of key first, nodes without a value last. */
export function topBy(nodes: NodeTrends[], key: TrendKey): NodeTrends[] {
	return [...nodes].sort(
		(a, b) =>
			(b[key]?.recent ?? -1) - (a[key]?.recent ?? -1) || a.hostname.localeCompare(b.hostname)
	);
}

/** Days until full from now (0 when overdue), undefined without a forecast. */
export function daysUntil(fullAt: number | undefined, now: number): number | undefined {
	return fullAt === undefined ? undefined : Math.max(0, (fullAt - now) / 86_400_000);
}
