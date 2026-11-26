/**
 * What a widget shows, independent of where it comes from: the UI asks the existing readers with
 * the viewer's session (sessionSource), the kiosk asks the KioskService for a widget of its
 * dashboard (M5-26). The widgets only render a WidgetResult.
 */
import type { Alert } from '#lib/api/gen/sermon/web/v1/alert_pb.js';
import type { Widget } from '#lib/api/gen/sermon/web/v1/dashboard_pb.js';
import type { LogLine } from '#lib/api/gen/sermon/web/v1/log_pb.js';
import type { Unit } from '#lib/format/format.js';
import type { Range } from '#lib/stores/timerange.svelte.js';

/** One point: time in ms since the epoch; min and max of its bucket. */
export interface SeriesPoint {
	time: number;
	value: number;
	min: number;
	max: number;
}

/** One series of a metric of one node. */
export interface WidgetSeries {
	nodeId: string;
	hostname: string;
	labels: Record<string, string>;
	points: SeriesPoint[];
}

export type WidgetResult =
	| { kind: 'metrics'; unit: Unit; series: WidgetSeries[] }
	| { kind: 'logs'; lines: LogLine[]; more: boolean }
	| { kind: 'alerts'; alerts: Alert[] };

export interface WidgetSource {
	load(widget: Widget, range: Range): Promise<WidgetResult>;
}

/** The latest finite value of a series, if any. */
export function latest(s: WidgetSeries): SeriesPoint | undefined {
	for (let i = s.points.length - 1; i >= 0; i--)
		if (Number.isFinite(s.points[i].value)) return s.points[i];
	return undefined;
}

/** "host mode=user" – the hostname and the label values that tell series apart. */
export function seriesName(s: WidgetSeries, filtered: Record<string, string> = {}): string {
	const values = Object.entries(s.labels)
		.filter(([name]) => !(name in filtered))
		.sort(([a], [b]) => a.localeCompare(b))
		.map(([, v]) => v);
	return [s.hostname || s.nodeId, ...values].join(' ');
}

const units: Unit[] = [
	'ratio',
	'ratio_ppm',
	'bytes',
	'bytes_per_second',
	'bits_per_second',
	'per_second',
	'pages_per_second',
	'count',
	'seconds',
	'timestamp_seconds',
	'celsius',
	'load',
	'boolean'
];

/** The unit of the catalog as a unit of the formatter; unknown ones count. */
export function unitOf(name: string): Unit {
	return (units as string[]).includes(name) ? (name as Unit) : 'count';
}
