/**
 * Failed logins as stacked bars per node (KONZEPT §16.4): the busiest nodes each get a colour,
 * the others share one, so the chart stays readable with a hundred nodes.
 */

/** The counts of one node; at is the start of its bucket in ms. */
export interface NodeCounts {
	node: string;
	points: { at: number; count: number }[];
}

export interface StackBar {
	start: number;
	end: number;
	/** One count per entry of the legend. */
	counts: number[];
	total: number;
}

/** An entry of the legend: a node, or the rest (node undefined). */
export interface StackEntry {
	node?: string;
	total: number;
}

export interface Stack {
	bars: StackBar[];
	legend: StackEntry[];
}

/** Nodes with their own colour. */
export const MAX_STACKED = 6;
/** Bounds the number of bars, also for a step that doesn't fit the range. */
const MAX_BARS = 400;

export function stack(
	series: NodeCounts[],
	step: number,
	range: { from: number; to: number },
	maxNodes = MAX_STACKED
): Stack {
	if (!(step > 0) || range.to <= range.from) return { bars: [], legend: [] };
	const width = Math.max(step, (range.to - range.from) / MAX_BARS);
	const n = Math.ceil((range.to - range.from) / width);
	const inRange = (p: { at: number; count: number }) =>
		Number.isFinite(p.count) && p.at >= range.from && p.at < range.to;
	const totals = series
		.map((s) => ({
			node: s.node,
			total: s.points.reduce((sum, p) => sum + (inRange(p) ? p.count : 0), 0)
		}))
		.filter((s) => s.total > 0)
		.sort((a, b) => b.total - a.total || a.node.localeCompare(b.node));
	const named = totals.slice(0, maxNodes);
	const rest = totals.slice(maxNodes).reduce((sum, s) => sum + s.total, 0);
	const legend: StackEntry[] = rest > 0 ? [...named, { total: rest }] : named;
	const index = new Map(named.map((s, i) => [s.node, i]));
	const bars: StackBar[] = Array.from({ length: n }, (_, i) => ({
		start: range.from + i * width,
		end: Math.min(range.from + (i + 1) * width, range.to),
		counts: legend.map(() => 0),
		total: 0
	}));
	for (const s of series) {
		const entry = index.get(s.node) ?? (rest > 0 ? legend.length - 1 : -1);
		if (entry < 0) continue;
		for (const p of s.points) {
			if (!inRange(p)) continue;
			const bar = bars[Math.min(Math.floor((p.at - range.from) / width), n - 1)];
			bar.counts[entry] += p.count;
			bar.total += p.count;
		}
	}
	return { bars, legend };
}
