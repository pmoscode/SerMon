<script lang="ts">
	import LayoutGrid from '@lucide/svelte/icons/layout-grid';
	import Server from '@lucide/svelte/icons/server';
	import Plus from '@lucide/svelte/icons/plus';
	import Table from '@lucide/svelte/icons/table';
	import { resolve } from '$app/paths';
	import FleetHeader from '#lib/components/fleet/FleetHeader.svelte';
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

	const nodes = $derived(fleet.summary?.nodes ?? []);
	const shown = $derived(filterNodes(nodes, { search, group, status, tag }));
	const counts = $derived(countByStatus(nodes));
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

<div class="mb-6 flex flex-wrap items-center gap-2">
	<h1 class="text-2xl font-semibold tracking-tight">{m.fleet_title()}</h1>
	<HelpTip topic="pages/fleet" />
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
