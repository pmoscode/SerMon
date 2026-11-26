<script lang="ts">
	import { untrack } from 'svelte';
	import { WidgetKind, type Widget } from '#lib/api/gen/sermon/web/v1/dashboard_pb.js';
	import type { WidgetResult, WidgetSource } from '#lib/dashboards/source.js';
	import { m } from '#lib/paraglide/messages.js';
	import type { Range } from '#lib/stores/timerange.svelte.js';

	/**
	 * One widget's content: loads it from the source for the range and renders its kind, each
	 * loaded on demand. A failed refresh keeps the last data and says so (onstale).
	 */
	let {
		widget,
		source,
		range,
		height,
		onstatus
	}: {
		widget: Widget;
		source: WidgetSource;
		range: Range;
		/** The pixels the content may use. */
		height: number;
		/** Called after every load: ok or failed (the kiosk shows stale data). */
		onstatus?: (ok: boolean) => void;
	} = $props();

	let result = $state.raw<WidgetResult | undefined>();
	let failed = $state(false);
	let request = 0;

	$effect(() => {
		const r = range;
		const w = widget;
		const current = ++request;
		source
			.load(w, r)
			.then((next) => {
				if (current !== request) return;
				result = next;
				failed = false;
				untrack(() => onstatus?.(true));
			})
			.catch(() => {
				if (current !== request) return;
				failed = true;
				untrack(() => onstatus?.(false));
			});
	});

	const labels = $derived(widget.query?.labels ?? {});
	// The chart (uPlot) on its own, the other kinds in two small groups.
	const chart = () => import('./widgets/TimeseriesWidget.svelte');
	const values = () => import('./widgets/values.js');
	const lists = () => import('./widgets/lists.js');
</script>

{#if !result}
	{#if failed}
		<p role="alert" class="text-sm text-destructive">{m.widget_failed()}</p>
	{:else}
		<p role="status" class="text-sm text-muted-foreground">{m.loading()}</p>
	{/if}
{:else}
	{#if failed}
		<p role="alert" class="mb-1 text-xs text-amber-700 dark:text-amber-400">{m.widget_stale()}</p>
	{/if}
	{#if result.kind === 'metrics'}
		{#if widget.kind === WidgetKind.TIMESERIES}
			{#await chart() then { default: C }}
				<C title={widget.title} unit={result.unit} series={result.series} {labels} {height} />
			{/await}
		{:else if widget.kind === WidgetKind.GAUGE}
			{#await values() then { GaugeWidget: C }}
				<C unit={result.unit} series={result.series} {labels} />
			{/await}
		{:else if widget.kind === WidgetKind.STAT}
			{#await values() then { StatWidget: C }}
				<C unit={result.unit} series={result.series} {labels} />
			{/await}
		{:else}
			{#await values() then { TableWidget: C }}
				<C title={widget.title} unit={result.unit} series={result.series} {labels} />
			{/await}
		{/if}
	{:else if result.kind === 'logs'}
		{#await lists() then { LogStreamWidget: C }}
			<C title={widget.title} lines={result.lines} more={result.more} />
		{/await}
	{:else}
		{#await lists() then { AlertListWidget: C }}
			<C title={widget.title} alerts={result.alerts} />
		{/await}
	{/if}
{/if}
