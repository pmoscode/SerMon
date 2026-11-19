import type { Range } from '#lib/stores/timerange.svelte.js';

/** One point of a chart series: time in ms since the epoch. */
export interface ChartPoint {
	time: number;
	value: number;
}

/** One line of a chart. */
export interface ChartSeries {
	label: string;
	points: ChartPoint[];
}

/** uPlot's columnar data: x in seconds, then one column per series with gaps as null. */
export type Aligned = [number[], ...(number | null)[][]];

/** Aligns series with different timestamps (several nodes) on one sorted x axis. */
export function align(series: ChartSeries[]): Aligned {
	const times = [...new Set(series.flatMap((s) => s.points.map((p) => p.time)))].sort(
		(a, b) => a - b
	);
	const index = new Map(times.map((t, i) => [t, i]));
	const columns = series.map((s) => {
		const column: (number | null)[] = new Array(times.length).fill(null);
		for (const p of s.points)
			column[index.get(p.time)!] = Number.isFinite(p.value) ? p.value : null;
		return column;
	});
	return [times.map((t) => t / 1000), ...columns];
}

/** Minimum, maximum and latest value of a series within the range. */
export interface Summary {
	label: string;
	min: number;
	max: number;
	last: number;
}

/** Summaries of the series within the range, for screen readers; series without points are left out. */
export function summarize(series: ChartSeries[], range: Range): Summary[] {
	const out: Summary[] = [];
	for (const s of series) {
		const values = s.points
			.filter((p) => p.time >= range.from && p.time < range.to && Number.isFinite(p.value))
			.sort((a, b) => a.time - b.time)
			.map((p) => p.value);
		if (values.length === 0) continue;
		out.push({
			label: s.label,
			min: Math.min(...values),
			max: Math.max(...values),
			last: values.at(-1)!
		});
	}
	return out;
}
