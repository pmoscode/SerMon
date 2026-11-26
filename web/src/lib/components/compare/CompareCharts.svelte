<script lang="ts">
	import TimeSeries from '#lib/charts/TimeSeries.svelte';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import type { CompareResult } from '#lib/compare/compare.svelte.js';
	import { compareMetrics, sharedMax, type CompareView } from '#lib/compare/compare.js';
	import { displayText } from '#lib/logs/text.js';
	import { m } from '#lib/paraglide/messages.js';

	/**
	 * The charts of a comparison (M5-28): per metric one chart with all nodes, or one chart per node
	 * next to each other on the same scale. All charts share the page's time range and crosshair.
	 */
	let {
		nodes,
		view,
		results,
		loading
	}: {
		nodes: { id: string; hostname: string }[];
		view: CompareView;
		results: Record<string, CompareResult>;
		loading: boolean;
	} = $props();

	const titles: Record<string, () => string> = {
		cpu: () => m.compare_metric_cpu(),
		memory: () => m.compare_metric_memory(),
		load: () => m.compare_metric_load(),
		disk: () => m.compare_metric_disk(),
		receive: () => m.compare_metric_receive(),
		transmit: () => m.compare_metric_transmit()
	};

	const loaded = $derived(!loading && Object.keys(results).length > 0);
	// Nodes without a single point in any metric: offline in the range, or never reported.
	const withoutData = $derived(
		loaded
			? nodes.filter(
					(n) => !Object.values(results).some((r) => (r.perNode.get(n.id)?.length ?? 0) > 0)
				)
			: []
	);
	const columns: Record<number, string> = {
		2: 'md:grid-cols-2',
		3: 'md:grid-cols-2 xl:grid-cols-3',
		4: 'md:grid-cols-2 xl:grid-cols-4'
	};
	const errorText = (r: CompareResult | undefined) => (r?.error ? m.error_unavailable() : '');
</script>

<div class="flex flex-col gap-6">
	{#if withoutData.length > 0}
		<p role="note" class="text-sm text-muted-foreground">
			{m.compare_no_data({ hosts: withoutData.map((n) => displayText(n.hostname)).join(', ') })}
		</p>
	{/if}
	{#each compareMetrics as metric (metric.key)}
		{@const result = results[metric.key]}
		{@const title = titles[metric.key]()}
		{#if view === 'overlay'}
			<TimeSeries
				{title}
				unit={metric.unit}
				height={200}
				{loading}
				error={errorText(result)}
				series={nodes.map((n) => ({
					label: displayText(n.hostname),
					points: result?.perNode.get(n.id) ?? []
				}))}
			>
				{#snippet actions()}
					<HelpTip topic={metric.help} />
				{/snippet}
			</TimeSeries>
		{:else}
			{@const top = result ? sharedMax(result.perNode) : undefined}
			<section class="flex flex-col gap-2" aria-label={title}>
				<h2 class="flex items-center gap-1 text-sm font-medium">
					{title}
					<HelpTip topic={metric.help} />
				</h2>
				<div class="grid gap-4 {columns[nodes.length] ?? 'md:grid-cols-2'}">
					{#each nodes as n (n.id)}
						<TimeSeries
							title={m.compare_chart({ metric: title, host: displayText(n.hostname) })}
							unit={metric.unit}
							height={160}
							{loading}
							error={errorText(result)}
							yMax={top}
							series={[{ label: displayText(n.hostname), points: result?.perNode.get(n.id) ?? [] }]}
						/>
					{/each}
				</div>
			</section>
		{/if}
	{/each}
</div>
