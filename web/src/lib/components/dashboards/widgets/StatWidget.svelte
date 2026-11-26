<script lang="ts">
	import Sparkline from '#lib/components/fleet/Sparkline.svelte';
	import { latest, seriesName, type WidgetSeries } from '#lib/dashboards/source.js';
	import { formatValue, type Unit } from '#lib/format/format.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { displayText } from '#lib/logs/text.js';
	import { m } from '#lib/paraglide/messages.js';

	/** The latest value of one node with a sparkline; several series (labels) at most MAX_ROWS. */
	const MAX_ROWS = 4;
	let {
		unit,
		series,
		labels = {}
	}: { unit: Unit; series: WidgetSeries[]; labels?: Record<string, string> } = $props();

	const rows = $derived(
		series.flatMap((s) => {
			const p = latest(s);
			if (!p) return [];
			const values = s.points.map((x) => x.value).filter(Number.isFinite);
			const lo = Math.min(...values);
			const hi = Math.max(...values);
			const spread = hi - lo || 1;
			return [
				{
					name: displayText(seriesName(s, labels)),
					text: formatValue(unit, p.value, localeState.current),
					spark: values.slice(-120).map((v) => (v - lo) / spread)
				}
			];
		})
	);
</script>

{#if rows.length === 0}
	<p class="text-sm text-muted-foreground">{m.widget_no_data()}</p>
{:else if rows.length === 1}
	<div class="flex h-full flex-col justify-center gap-1">
		<p class="text-3xl font-semibold tabular-nums">{rows[0].text}</p>
		<Sparkline values={rows[0].spark} />
	</div>
{:else}
	<ul class="flex flex-col gap-1 text-sm">
		{#each rows.slice(0, MAX_ROWS) as r, i (i)}
			<li class="flex items-baseline gap-2">
				<span class="min-w-0 flex-1 truncate text-muted-foreground">{r.name}</span>
				<span class="font-semibold tabular-nums">{r.text}</span>
			</li>
		{/each}
		{#if rows.length > MAX_ROWS}
			<li class="text-xs text-muted-foreground">
				{m.widget_more_series({ count: rows.length - MAX_ROWS })}
			</li>
		{/if}
	</ul>
{/if}
