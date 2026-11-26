<script lang="ts">
	import { latest, type WidgetSeries } from '#lib/dashboards/source.js';
	import { formatValue, type Unit } from '#lib/format/format.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { displayText } from '#lib/logs/text.js';
	import { m } from '#lib/paraglide/messages.js';

	/** The latest value, minimum and maximum of the range per node and series; at most MAX_ROWS rows. */
	const MAX_ROWS = 200;
	let {
		title,
		unit,
		series,
		labels = {}
	}: {
		title: string;
		unit: Unit;
		series: WidgetSeries[];
		labels?: Record<string, string>;
	} = $props();

	const format = (v: number) =>
		Number.isFinite(v) ? formatValue(unit, v, localeState.current) : '–';
	const rows = $derived(
		series
			.map((s) => {
				const values = s.points.filter((p) => Number.isFinite(p.value));
				return {
					host: displayText(s.hostname || s.nodeId),
					labels: displayText(
						Object.entries(s.labels)
							.filter(([k]) => !(k in labels))
							.sort(([a], [b]) => a.localeCompare(b))
							.map(([k, v]) => `${k}=${v}`)
							.join(' ')
					),
					last: latest(s)?.value ?? NaN,
					min: Math.min(...values.map((p) => p.min).filter(Number.isFinite)),
					max: Math.max(...values.map((p) => p.max).filter(Number.isFinite))
				};
			})
			.sort((a, b) => a.host.localeCompare(b.host) || a.labels.localeCompare(b.labels))
	);
</script>

{#if rows.length === 0}
	<p class="text-sm text-muted-foreground">{m.widget_no_data()}</p>
{:else}
	<table class="w-full text-sm">
		<caption class="sr-only">{title}</caption>
		<thead class="text-left text-xs text-muted-foreground">
			<tr>
				<th scope="col" class="font-medium">{m.widget_table_node()}</th>
				<th scope="col" class="font-medium">{m.widget_table_series()}</th>
				<th scope="col" class="text-right font-medium">{m.widget_table_last()}</th>
				<th scope="col" class="text-right font-medium">{m.widget_table_min()}</th>
				<th scope="col" class="text-right font-medium">{m.widget_table_max()}</th>
			</tr>
		</thead>
		<tbody>
			{#each rows.slice(0, MAX_ROWS) as r, i (i)}
				<tr class="border-t">
					<td class="max-w-40 truncate">{r.host}</td>
					<td class="max-w-40 truncate text-muted-foreground">{r.labels}</td>
					<td class="text-right tabular-nums">{format(r.last)}</td>
					<td class="text-right tabular-nums">{format(r.min)}</td>
					<td class="text-right tabular-nums">{format(r.max)}</td>
				</tr>
			{/each}
		</tbody>
	</table>
	{#if rows.length > MAX_ROWS}
		<p class="text-xs text-muted-foreground">
			{m.widget_more_rows({ count: rows.length - MAX_ROWS })}
		</p>
	{/if}
{/if}
