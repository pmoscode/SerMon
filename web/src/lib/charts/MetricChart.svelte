<script lang="ts">
	import { untrack } from 'svelte';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import type { Unit } from '#lib/format/format.js';
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
	{/snippet}
</TimeSeries>
