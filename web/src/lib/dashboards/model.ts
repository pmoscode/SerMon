/**
 * The document of a custom dashboard in the UI (ADR 0040): what a new widget looks like, how the
 * grid layout maps to the widgets and how the server's preset maps to the page's time range.
 */
import { create } from '@bufbuild/protobuf';
import {
	DashboardSpecSchema,
	TimePreset,
	WidgetKind,
	WidgetPositionSchema,
	WidgetQuerySchema,
	WidgetSchema,
	WidgetTargetSchema,
	type DashboardSpec,
	type Widget
} from '#lib/api/gen/sermon/web/v1/dashboard_pb.js';
import type { Preset } from '#lib/stores/timerange.svelte.js';
import { firstFree, type Item } from './layout.js';

export const MAX_WIDGETS = 30;
export const MAX_VARIABLES = 4;
export const MAX_NODES = 20;

/** The kinds in the order the editor offers them, with the size a new widget gets. */
export const kinds: { kind: WidgetKind; w: number; h: number; metric: boolean }[] = [
	{ kind: WidgetKind.TIMESERIES, w: 6, h: 4, metric: true },
	{ kind: WidgetKind.GAUGE, w: 3, h: 3, metric: true },
	{ kind: WidgetKind.STAT, w: 3, h: 2, metric: true },
	{ kind: WidgetKind.TABLE, w: 6, h: 4, metric: true },
	{ kind: WidgetKind.LOG_STREAM, w: 12, h: 4, metric: false },
	{ kind: WidgetKind.ALERT_LIST, w: 6, h: 3, metric: false }
];

export function isMetricKind(kind: WidgetKind): boolean {
	return kinds.some((k) => k.kind === kind && k.metric);
}

/** A gauge or stat shows one node. */
export function singleNode(kind: WidgetKind): boolean {
	return kind === WidgetKind.GAUGE || kind === WidgetKind.STAT;
}

export function newSpec(title: string): DashboardSpec {
	return create(DashboardSpecSchema, { version: 1, title, timeRange: TimePreset.TIME_PRESET_1H });
}

/** An unused widget ID derived from the kind ("timeseries", "timeseries-2", …). */
export function newWidgetId(spec: DashboardSpec, kind: WidgetKind): string {
	const base = (WidgetKind[kind] ?? 'widget').toLowerCase().replaceAll('_', '-').slice(0, 24);
	const used = new Set(spec.widgets.map((w) => w.id));
	if (!used.has(base)) return base;
	for (let i = 2; ; i++) if (!used.has(`${base}-${i}`)) return `${base}-${i}`;
}

/** A new widget of the kind at the first free place, or undefined if the grid or limit is full. */
export function newWidget(
	spec: DashboardSpec,
	kind: WidgetKind,
	title: string
): Widget | undefined {
	if (spec.widgets.length >= MAX_WIDGETS) return undefined;
	const size = kinds.find((k) => k.kind === kind) ?? kinds[0];
	const place = firstFree(layoutOf(spec), size.w, size.h);
	if (!place) return undefined;
	return create(WidgetSchema, {
		id: newWidgetId(spec, kind),
		kind,
		title,
		position: create(WidgetPositionSchema, place),
		query: create(WidgetQuerySchema, { target: create(WidgetTargetSchema) })
	});
}

/** The boxes of the widgets for the grid. */
export function layoutOf(spec: DashboardSpec): Item[] {
	return spec.widgets.map((w) => ({
		id: w.id,
		x: w.position?.x ?? 0,
		y: w.position?.y ?? 0,
		w: w.position?.w ?? 1,
		h: w.position?.h ?? 1
	}));
}

/** The widgets moved to the boxes of the layout. */
export function withLayout(spec: DashboardSpec, layout: Item[]): DashboardSpec {
	const boxes = new Map(layout.map((i) => [i.id, i]));
	return {
		...spec,
		widgets: spec.widgets.map((w) => {
			const b = boxes.get(w.id);
			return b
				? { ...w, position: create(WidgetPositionSchema, { x: b.x, y: b.y, w: b.w, h: b.h }) }
				: w;
		})
	};
}

const presets: Record<TimePreset, Preset> = {
	[TimePreset.TIME_PRESET_UNSPECIFIED]: '1h',
	[TimePreset.TIME_PRESET_15M]: '15m',
	[TimePreset.TIME_PRESET_1H]: '1h',
	[TimePreset.TIME_PRESET_6H]: '6h',
	[TimePreset.TIME_PRESET_24H]: '24h',
	[TimePreset.TIME_PRESET_7D]: '7d',
	[TimePreset.TIME_PRESET_30D]: '30d'
};

/** The page's preset of the dashboard's default range. */
export function presetOf(p: TimePreset): Preset {
	return presets[p] ?? '1h';
}

/** The dashboard's default range of the page's preset. */
export function timePresetOf(p: Preset): TimePreset {
	const found = Object.entries(presets).find(
		([k, v]) => v === p && Number(k) !== TimePreset.TIME_PRESET_UNSPECIFIED
	);
	return found ? (Number(found[0]) as TimePreset) : TimePreset.TIME_PRESET_1H;
}
