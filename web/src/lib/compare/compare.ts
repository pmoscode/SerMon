/**
 * The comparison view (KONZEPT §16, 8; M5-28): two to four nodes side by side or overlaid on the
 * same time range. The selection lives in the URL, so a comparison can be shared; invalid values
 * are dropped, the server checks every node again.
 */
import type { Unit } from '#lib/format/format.js';
import type { ChartPoint } from '#lib/charts/data.js';

export const MIN_NODES = 2;
export const MAX_NODES = 4;

/** Side by side: one chart per node with the same scale; overlay: one chart with all nodes. */
export type CompareView = 'side' | 'overlay';

export interface CompareSelection {
	nodes: string[];
	view: CompareView;
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export function isNodeId(value: string): boolean {
	return UUID.test(value);
}

export function parseCompare(params: URLSearchParams): CompareSelection {
	let selection: CompareSelection = {
		nodes: [],
		view: params.get('view') === 'overlay' ? 'overlay' : 'side'
	};
	for (const id of params.getAll('node')) selection = addNode(selection, id);
	return selection;
}

export function compareParams(selection: CompareSelection): URLSearchParams {
	const params = new URLSearchParams();
	for (const id of selection.nodes) params.append('node', id);
	if (selection.view === 'overlay') params.set('view', 'overlay');
	return params;
}

/** Adds a node unless it is there already, no node ID, or four are chosen. */
export function addNode(selection: CompareSelection, id: string): CompareSelection {
	if (!isNodeId(id) || selection.nodes.includes(id) || selection.nodes.length >= MAX_NODES)
		return selection;
	return { ...selection, nodes: [...selection.nodes, id] };
}

export function removeNode(selection: CompareSelection, id: string): CompareSelection {
	return { ...selection, nodes: selection.nodes.filter((n) => n !== id) };
}

/** How the series of one node become one line: interfaces add up, the busiest disk counts. */
export type Combine = 'sum' | 'max';

export interface NodeSeries {
	nodeId: string;
	points: ChartPoint[];
}

/** One line per node: its series combined per time, values transformed, gaps left out. */
export function combine(
	series: NodeSeries[],
	how: Combine,
	transform: (value: number) => number = (v) => v
): Map<string, ChartPoint[]> {
	const byNode = new Map<string, Map<number, number>>();
	for (const s of series) {
		let times = byNode.get(s.nodeId);
		if (!times) byNode.set(s.nodeId, (times = new Map()));
		for (const p of s.points) {
			if (!Number.isFinite(p.value)) continue;
			const value = transform(p.value);
			const before = times.get(p.time);
			times.set(
				p.time,
				before === undefined ? value : how === 'sum' ? before + value : Math.max(before, value)
			);
		}
	}
	const out = new Map<string, ChartPoint[]>();
	for (const [node, times] of byNode)
		out.set(
			node,
			[...times].sort(([x], [y]) => x - y).map(([time, value]) => ({ time, value }))
		);
	return out;
}

/** The largest value of all nodes: side by side, every chart of a metric uses this scale. */
export function sharedMax(perNode: ReadonlyMap<string, ChartPoint[]>): number | undefined {
	let max: number | undefined;
	for (const points of perNode.values())
		for (const p of points) max = max === undefined ? p.value : Math.max(max, p.value);
	return max;
}

/** A metric of the comparison: one line per node. */
export interface CompareMetric {
	key: string;
	metric: string;
	labels?: Record<string, string>;
	unit: Unit;
	combine: Combine;
	transform?: (value: number) => number;
	/** The help topic of the ⓘ. */
	help: string;
}

/** The core metrics of the node page, each as one line per node. */
export const compareMetrics: CompareMetric[] = [
	{
		key: 'cpu',
		metric: 'sermon_cpu_utilization_ratio',
		labels: { mode: 'idle' },
		unit: 'ratio_ppm',
		combine: 'max',
		transform: (v) => Math.min(1_000_000, Math.max(0, 1_000_000 - v)),
		help: 'metrics/sermon_cpu_utilization_ratio'
	},
	{
		key: 'memory',
		metric: 'sermon_memory_utilization_ratio',
		unit: 'ratio_ppm',
		combine: 'max',
		help: 'metrics/sermon_memory_utilization_ratio'
	},
	{
		key: 'load',
		metric: 'sermon_load_average_per_cpu',
		labels: { period: '1m' },
		unit: 'load',
		combine: 'max',
		help: 'metrics/sermon_load_average_per_cpu'
	},
	{
		key: 'disk',
		metric: 'sermon_disk_busy_ratio',
		unit: 'ratio_ppm',
		combine: 'max',
		help: 'metrics/sermon_disk_busy_ratio'
	},
	{
		key: 'receive',
		metric: 'sermon_network_receive_bytes_per_second',
		unit: 'bytes_per_second',
		combine: 'sum',
		help: 'metrics/sermon_network_receive_bytes_per_second'
	},
	{
		key: 'transmit',
		metric: 'sermon_network_transmit_bytes_per_second',
		unit: 'bytes_per_second',
		combine: 'sum',
		help: 'metrics/sermon_network_transmit_bytes_per_second'
	}
];
