<script lang="ts">
	import type { ChartSeries } from '#lib/charts/data.js';
	import TimeSeries from '#lib/charts/TimeSeries.svelte';
	import { seriesName, type WidgetSeries } from '#lib/dashboards/source.js';
	import type { Unit } from '#lib/format/format.js';
	import { displayText } from '#lib/logs/text.js';
	import { m } from '#lib/paraglide/messages.js';

	/** A metric over the time range (uPlot), at most MAX_LINES lines. */
	const MAX_LINES = 40;
	let {
		title,
		unit,
		series,
		labels = {},
		height
	}: {
		title: string;
		unit: Unit;
		series: WidgetSeries[];
		labels?: Record<string, string>;
		height: number;
	} = $props();

	const lines = $derived<ChartSeries[]>(
		series.slice(0, MAX_LINES).map((s) => ({
			label: displayText(seriesName(s, labels)),
			points: s.points
				.filter((p) => Number.isFinite(p.time))
				.map((p) => ({ time: p.time, value: p.value }))
		}))
	);
	const hidden = $derived(Math.max(0, series.length - MAX_LINES));
</script>

<TimeSeries
	{title}
	{unit}
	series={lines}
	height={Math.max(60, height - (hidden ? 20 : 0))}
	quietTitle
/>
{#if hidden}
	<p class="text-xs text-muted-foreground">{m.widget_more_series({ count: hidden })}</p>
{/if}
