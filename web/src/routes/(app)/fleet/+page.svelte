<script lang="ts">
	import LayoutGrid from '@lucide/svelte/icons/layout-grid';
	import Server from '@lucide/svelte/icons/server';
	import Plus from '@lucide/svelte/icons/plus';
	import ShieldAlert from '@lucide/svelte/icons/shield-alert';
	import Table from '@lucide/svelte/icons/table';
	import Activity from '@lucide/svelte/icons/activity';
	import { untrack } from 'svelte';
	import { resolve } from '$app/paths';
	import { fleetRate, loadRates } from '#lib/anomalies/fleet.js';
	import FleetHeader from '#lib/components/fleet/FleetHeader.svelte';
	import FleetNav from '#lib/components/fleet/FleetNav.svelte';
	import NodeTable from '#lib/components/fleet/NodeTable.svelte';
	import NodeTile from '#lib/components/fleet/NodeTile.svelte';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { FleetState } from '#lib/fleet/fleet.svelte.js';
	import { countByStatus, filterNodes, groupsOf, tagsOf, type Status } from '#lib/fleet/status.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { m } from '#lib/paraglide/messages.js';
	import { sessionState } from '#lib/stores/session.svelte.js';

	const VIEW_KEY = 'sermon-fleet-view';
	type View = 'tiles' | 'table';

	const fleet = new FleetState();
	let search = $state('');
	let group = $state('');
	let status = $state<Status | ''>('');
	let tag = $state('');
	let view = $state<View>(readView());

	function readView(): View {
		try {
			return localStorage.getItem(VIEW_KEY) === 'table' ? 'table' : 'tiles';
		} catch {
			return 'tiles';
		}
	}

	function setView(next: View) {
		view = next;
		try {
			localStorage.setItem(VIEW_KEY, next);
		} catch {
			// Storage blocked: the choice lasts for this page only.
		}
	}

	$effect(() => fleet.start());

	// The fleet's anomaly rate (M4-25), every minute, in the header.
	let anomalyRate = $state<number | undefined>();
	$effect(() => {
		const run = () => {
			const ids = untrack(() => (fleet.summary?.nodes ?? []).map((n) => n.nodeId));
			if (ids.length === 0) return;
			loadRates(ids)
				.then((rates) => (anomalyRate = fleetRate(rates)))
				.catch(() => (anomalyRate = undefined));
		};
		const first = setTimeout(run, 2000);
		const timer = setInterval(run, 60_000);
		return () => {
			clearTimeout(first);
			clearInterval(timer);
		};
	});
	const percent = $derived(
		new Intl.NumberFormat(localeState.current, { style: 'percent', maximumFractionDigits: 1 })
	);

	const nodes = $derived(fleet.summary?.nodes ?? []);
	const shown = $derived(filterNodes(nodes, { search, group, status, tag }));
	const counts = $derived(countByStatus(nodes));
	// Nodes with security updates (M4-15), in the header with a link to the updates.
	const securityNodes = $derived(nodes.filter((n) => n.securityUpdates > 0).length);
	const groups = $derived(groupsOf(nodes));
	const tags = $derived(tagsOf(nodes));
	const now = $derived(new Date(fleet.updatedAt ?? Date.now()));
	const timeFormat = $derived(new Intl.DateTimeFormat(localeState.current, { timeStyle: 'short' }));

	function resetFilter() {
		search = '';
		group = '';
		status = '';
		tag = '';
	}
</script>

<svelte:head>
	<title>{m.fleet_title()} · SerMon</title>
</svelte:head>

<FleetNav />

<div class="mb-6 flex flex-wrap items-center gap-2">
	<h1 class="text-2xl font-semibold tracking-tight">{m.fleet_title()}</h1>
	<HelpTip topic="pages/fleet" />
	{#if securityNodes > 0}
		<a
			href={resolve('/(app)/updates')}
			class="inline-flex items-center gap-1 rounded-md border border-red-600/40 px-2 py-0.5 text-sm text-red-700 hover:underline dark:text-red-400"
		>
			<ShieldAlert class="size-4" aria-hidden="true" />
			{m.fleet_security_nodes({ count: securityNodes })}
		</a>
	{/if}
	{#if (sessionState.current?.enrollGroupIds.length ?? 0) > 0}
		<Button href={resolve('/(app)/nodes/add')} size="sm" class="ml-auto">
			<Plus aria-hidden="true" />
			{m.addnode_title()}
		</Button>
	{/if}
</div>

{#if fleet.status === 'loading'}
	<p role="status" class="text-muted-foreground">{m.loading()}</p>
{:else if fleet.status === 'error'}
	<div class="flex flex-col items-start gap-3">
		<p role="alert" class="text-destructive">{m.error_unavailable()}</p>
		<Button variant="outline" onclick={() => fleet.load()}>{m.retry()}</Button>
	</div>
{:else if nodes.length === 0}
	<section
		class="flex flex-col items-center gap-3 rounded-lg border border-dashed p-12 text-center"
		aria-labelledby="fleet-empty"
	>
		<Server class="size-10 text-muted-foreground" aria-hidden="true" />
		<h2 id="fleet-empty" class="text-lg font-medium">{m.fleet_empty_title()}</h2>
		<p class="max-w-md text-sm text-muted-foreground">{m.fleet_empty_text()}</p>
	</section>
{:else}
	<div class="flex flex-col gap-4">
		<FleetHeader total={nodes.length} {counts} active={status} onSelect={(s) => (status = s)} />
		{#if anomalyRate !== undefined}
			<a
				href={resolve('/(app)/anomalies')}
				class="inline-flex items-center gap-1 self-start text-sm text-muted-foreground underline-offset-2 hover:underline"
			>
				<Activity class="size-4" aria-hidden="true" />
				{m.anomalies_fleet_rate({ rate: percent.format(anomalyRate) })}
			</a>
		{/if}

		<div class="flex flex-wrap items-end gap-3" role="search" aria-label={m.fleet_filter()}>
			<div class="flex flex-col gap-1">
				<Label for="fleet-search">{m.fleet_search()}</Label>
				<Input
					id="fleet-search"
					type="search"
					class="w-64"
					bind:value={search}
					placeholder={m.fleet_search_hint()}
				/>
			</div>
			{#if groups.length > 1}
				<div class="flex flex-col gap-1">
					<Label for="fleet-group">{m.fleet_group()}</Label>
					<select
						id="fleet-group"
						class="h-9 rounded-md border border-input bg-background px-2 text-sm focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
						bind:value={group}
					>
						<option value="">{m.fleet_group_all()}</option>
						{#each groups as g (g.id)}
							<option value={g.id}>{g.name}</option>
						{/each}
					</select>
				</div>
			{/if}
			{#if tags.length > 0}
				<div class="flex flex-col gap-1">
					<Label for="fleet-tag">{m.fleet_tag()}</Label>
					<select
						id="fleet-tag"
						class="h-9 rounded-md border border-input bg-background px-2 font-mono text-sm focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
						bind:value={tag}
					>
						<option value="">{m.fleet_tag_all()}</option>
						{#each tags as pair (pair)}
							<option value={pair}>{pair}</option>
						{/each}
					</select>
				</div>
			{/if}
			<div class="ml-auto flex items-center gap-2">
				{#if fleet.stale && fleet.updatedAt}
					<p role="status" class="text-xs text-amber-700 dark:text-amber-400">
						{m.fleet_stale({ time: timeFormat.format(fleet.updatedAt) })}
					</p>
				{/if}
				<div role="group" aria-label={m.fleet_view()} class="inline-flex gap-1">
					<Button
						variant={view === 'tiles' ? 'secondary' : 'ghost'}
						size="icon-sm"
						aria-pressed={view === 'tiles'}
						aria-label={m.fleet_view_tiles()}
						title={m.fleet_view_tiles()}
						onclick={() => setView('tiles')}
					>
						<LayoutGrid aria-hidden="true" />
					</Button>
					<Button
						variant={view === 'table' ? 'secondary' : 'ghost'}
						size="icon-sm"
						aria-pressed={view === 'table'}
						aria-label={m.fleet_view_table()}
						title={m.fleet_view_table()}
						onclick={() => setView('table')}
					>
						<Table aria-hidden="true" />
					</Button>
				</div>
			</div>
		</div>

		<p class="sr-only" role="status">
			{m.fleet_shown({ shown: String(shown.length), total: String(nodes.length) })}
		</p>
		{#if shown.length === 0}
			<div class="flex flex-col items-start gap-3 rounded-lg border border-dashed p-6">
				<p>{m.fleet_no_match()}</p>
				<Button variant="outline" onclick={resetFilter}>{m.fleet_reset_filter()}</Button>
			</div>
		{:else if view === 'tiles'}
			<div class="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
				{#each shown as node (node.nodeId)}
					<NodeTile {node} sparkline={fleet.sparklines[node.nodeId]} {now} />
				{/each}
			</div>
		{:else}
			<NodeTable nodes={shown} sparklines={fleet.sparklines} {now} />
		{/if}
	</div>
{/if}
