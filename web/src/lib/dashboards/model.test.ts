import { describe, expect, it } from 'vitest';
import { TimePreset, WidgetKind } from '#lib/api/gen/sermon/web/v1/dashboard_pb.js';
import {
	layoutOf,
	MAX_WIDGETS,
	newSpec,
	newWidget,
	newWidgetId,
	presetOf,
	timePresetOf,
	withLayout
} from './model.js';

describe('dashboard model', () => {
	it('places new widgets at the first free place with the size of their kind', () => {
		const spec = newSpec('Web');
		const chart = newWidget(spec, WidgetKind.TIMESERIES, 'CPU')!;
		expect(chart.id).toBe('timeseries');
		expect(chart.position).toMatchObject({ x: 0, y: 0, w: 6, h: 4 });
		spec.widgets.push(chart);
		const second = newWidget(spec, WidgetKind.TIMESERIES, 'Memory')!;
		expect(second.id).toBe('timeseries-2');
		expect(second.position).toMatchObject({ x: 6, y: 0 });
		spec.widgets.push(second);
		expect(newWidget(spec, WidgetKind.LOG_STREAM, 'Logs')!.position).toMatchObject({
			x: 0,
			y: 4,
			w: 12
		});
	});

	it('stops at the limit of widgets', () => {
		const spec = newSpec('Full');
		for (let i = 0; i < MAX_WIDGETS; i++)
			spec.widgets.push(newWidget(spec, WidgetKind.STAT, `S${i}`)!);
		expect(newWidget(spec, WidgetKind.STAT, 'one more')).toBeUndefined();
		expect(new Set(spec.widgets.map((w) => w.id)).size).toBe(MAX_WIDGETS);
	});

	it('names widget IDs after the kind within the pattern of the server', () => {
		const spec = newSpec('x');
		expect(newWidgetId(spec, WidgetKind.ALERT_LIST)).toBe('alert-list');
		expect(newWidgetId(spec, WidgetKind.ALERT_LIST)).toMatch(/^[a-z0-9_-]{1,32}$/);
	});

	it('maps the layout to the widgets and back', () => {
		const spec = newSpec('x');
		spec.widgets.push(newWidget(spec, WidgetKind.GAUGE, 'G')!);
		const moved = withLayout(spec, [{ id: 'gauge', x: 4, y: 2, w: 5, h: 6 }]);
		expect(layoutOf(moved)).toEqual([{ id: 'gauge', x: 4, y: 2, w: 5, h: 6 }]);
		expect(spec.widgets[0].position).toMatchObject({ x: 0, y: 0 });
	});

	it('maps the default range to the page presets', () => {
		expect(presetOf(TimePreset.TIME_PRESET_24H)).toBe('24h');
		expect(presetOf(TimePreset.TIME_PRESET_UNSPECIFIED)).toBe('1h');
		expect(timePresetOf('7d')).toBe(TimePreset.TIME_PRESET_7D);
		expect(timePresetOf('1h')).toBe(TimePreset.TIME_PRESET_1H);
	});
});
