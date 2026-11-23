<script lang="ts">
	import ScrollText from '@lucide/svelte/icons/scroll-text';
	import { untrack } from 'svelte';
	import { resolve } from '$app/paths';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import type { Unit } from '#lib/format/format.js';
	import { logsQuery } from '#lib/logs/filter.js';
	import { m } from '#lib/paraglide/messages.js';
	import { timeRange } from '#lib/stores/timerange.svelte.js';
	import type { ChartSeries } from './data.js';
	import { loadSeries, type ChartQuery } from './query.js';
	import TimeSeries from './TimeSeries.svelte';

	/** A chart of one node: loads its queries for the global time range and follows it. */
	let {
		nodeId,
		title,
		unit,
		queries,
		bits = false,
		height = 180
	}: {
		nodeId: string;
		title: string;
		unit: Unit;
		queries: ChartQuery[];
		bits?: boolean;
		height?: number;
	} = $props();

	let series = $state<ChartSeries[]>([]);
	let loading = $state(true);
	let error = $state('');
	let request = 0;

	$effect(() => {
		const range = timeRange.range;
		const current = ++request;
		// The first load shows the spinner; refreshes keep the chart (untracked: no loop).
		if (untrack(() => series.length === 0)) loading = true;
		loadSeries(nodeId, queries, range, title)
			.then((next) => {
				if (current !== request) return;
				series = next;
				error = '';
			})
			.catch(() => {
				if (current === request) error = m.error_unavailable();
			})
			.finally(() => {
				if (current === request) loading = false;
			});
	});
</script>

<TimeSeries {title} {unit} {series} {bits} {height} {loading} {error}>
	{#snippet actions()}
		<HelpTip topic={'metrics/' + queries[0].metric} />
		<!-- eslint-disable svelte/no-navigation-without-resolve -- resolve() with the query of the filter -->
		<a
			href={`${resolve('/(app)/logs')}${logsQuery(nodeId, timeRange.range.from, timeRange.range.to)}`}
			class="ml-auto inline-flex items-center rounded-sm p-0.5 text-muted-foreground hover:text-foreground focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
			aria-label={m.chart_logs_link()}
			title={m.chart_logs_link()}
		>
			<ScrollText class="size-3.5" aria-hidden="true" />
		</a>
		<!-- eslint-enable svelte/no-navigation-without-resolve -->
	{/snippet}
</TimeSeries>
