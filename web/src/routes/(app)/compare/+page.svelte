<script lang="ts">
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import { api } from '#lib/api/client.js';
	import type { NodeSummary } from '#lib/api/gen/sermon/web/v1/fleet_pb.js';
	import CompareCharts from '#lib/components/compare/CompareCharts.svelte';
	import NodePicker from '#lib/components/compare/NodePicker.svelte';
	import FleetNav from '#lib/components/fleet/FleetNav.svelte';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import TimeRangePicker from '#lib/components/time/TimeRangePicker.svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import { CompareState } from '#lib/compare/compare.svelte.js';
	import {
		addNode,
		compareMetrics,
		compareParams,
		MIN_NODES,
		parseCompare,
		removeNode,
		type CompareSelection,
		type CompareView
	} from '#lib/compare/compare.js';
	import { m } from '#lib/paraglide/messages.js';
	import { sessionState } from '#lib/stores/session.svelte.js';
	import { timeRange } from '#lib/stores/timerange.svelte.js';

	/** The comparison (KONZEPT §16, 8; M5-28): the selection lives in the URL, so it can be shared. */
	const LIVE_INTERVAL = 10_000;

	const selection = $derived(parseCompare(new URLSearchParams(page.url.search)));
	let fleet = $state<NodeSummary[]>([]);
	let status = $state<'loading' | 'ready' | 'error'>('loading');
	const compare = new CompareState();

	async function loadFleet() {
		status = 'loading';
		try {
			fleet = (await api.fleet.getFleetSummary({})).nodes;
			status = 'ready';
		} catch {
			status = 'error';
		}
	}
	void loadFleet();

	function apply(next: CompareSelection) {
		const query = compareParams(next).toString();
		// eslint-disable-next-line svelte/no-navigation-without-resolve -- resolve() with the query of the selection
		void goto(`${resolve('/(app)/compare')}${query ? `?${query}` : ''}`, {
			replace: true,
			reset: false
		});
	}

	// Only nodes of the fleet are asked for: an unknown ID (deleted, other org) stays out.
	const known = $derived(
		selection.nodes.flatMap((id) => {
			const n = fleet.find((f) => f.nodeId === id);
			return n ? [{ id, hostname: n.hostname }] : [];
		})
	);
	const ids = $derived(known.map((n) => n.id).join(','));

	$effect(() => {
		const range = timeRange.range;
		const nodeIds = ids ? ids.split(',') : [];
		void compare.load(nodeIds.length >= MIN_NODES ? nodeIds : [], compareMetrics, range);
	});
	$effect(() => {
		const timer = setInterval(() => timeRange.refresh(), LIVE_INTERVAL);
		return () => clearInterval(timer);
	});

	const views: { view: CompareView; label: () => string }[] = [
		{ view: 'side', label: () => m.compare_view_side() },
		{ view: 'overlay', label: () => m.compare_view_overlay() }
	];
</script>

<svelte:head>
	<title>{m.compare_title()} · SerMon</title>
</svelte:head>

<FleetNav />

<div class="flex flex-col gap-4 pt-4">
	<div class="flex flex-wrap items-baseline gap-2">
		<h1 class="text-2xl font-semibold tracking-tight">{m.compare_title()}</h1>
		<HelpTip topic="pages/compare" />
	</div>
	<p class="text-sm text-muted-foreground">{m.compare_intro()}</p>

	{#if status === 'loading'}
		<p role="status" class="text-muted-foreground">{m.loading()}</p>
	{:else if status === 'error'}
		<div class="flex flex-col items-start gap-3">
			<p role="alert" class="text-destructive">{m.error_unavailable()}</p>
			<Button variant="outline" onclick={() => void loadFleet()}>{m.retry()}</Button>
		</div>
	{:else if fleet.length === 0}
		<div class="flex flex-col items-start gap-1">
			<p class="text-sm text-muted-foreground">{m.compare_no_nodes()}</p>
			{#if (sessionState.current?.enrollGroupIds.length ?? 0) > 0}
				<Button href={resolve('/(app)/nodes/add')} variant="link" size="sm" class="px-0">
					{m.addnode_title()}
				</Button>
			{/if}
		</div>
	{:else}
		<NodePicker
			options={fleet}
			selected={selection.nodes}
			onadd={(id) => apply(addNode(selection, id))}
			onremove={(id) => apply(removeNode(selection, id))}
		/>
		<div class="flex flex-wrap items-center justify-between gap-2">
			<TimeRangePicker />
			<div class="flex gap-1" role="group" aria-label={m.compare_view()}>
				{#each views as v (v.view)}
					<Button
						variant="outline"
						size="sm"
						aria-pressed={selection.view === v.view}
						class="aria-pressed:border-primary aria-pressed:ring-2 aria-pressed:ring-primary/30"
						onclick={() => apply({ ...selection, view: v.view })}
					>
						{v.label()}
					</Button>
				{/each}
			</div>
		</div>
		{#if known.length >= MIN_NODES}
			<CompareCharts
				nodes={known}
				view={selection.view}
				results={compare.results}
				loading={compare.loading}
			/>
		{/if}
	{/if}
</div>
