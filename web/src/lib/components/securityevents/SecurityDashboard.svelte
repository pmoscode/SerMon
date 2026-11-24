<script lang="ts">
	import Download from '@lucide/svelte/icons/download';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import { durationMs } from '@bufbuild/protobuf/wkt';
	import { untrack } from 'svelte';
	import { resolve } from '$app/paths';
	import { api, Code, codeOf } from '#lib/api/client.js';
	import type { NodeSummary } from '#lib/api/gen/sermon/web/v1/fleet_pb.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { formatNumber } from '#lib/format/format.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { m } from '#lib/paraglide/messages.js';
	import { SecurityDashboardState } from '#lib/security/dashboard.svelte.js';
	import { failedKinds, type SecurityFilter } from '#lib/security/filter.js';
	import { sessionState } from '#lib/stores/session.svelte.js';
	import FailedLoginsChart from './FailedLoginsChart.svelte';
	import SecurityEventTable from './SecurityEventTable.svelte';
	import SecurityFilters from './SecurityFilters.svelte';
	import SecurityTotals from './SecurityTotals.svelte';
	import TopList from './TopList.svelte';

	/**
	 * The security dashboard (KONZEPT §16.4): failed logins over time by node, the top addresses
	 * and users, totals, and the events of the filter with CSV export. The filter belongs to the
	 * caller: the page keeps it in the URL, the node page in its tab.
	 */
	let {
		filter,
		onFilter,
		fixedNode
	}: {
		filter: SecurityFilter;
		onFilter: (filter: SecurityFilter) => void;
		/** On a node page: the node, without node choice. */
		fixedNode?: string;
	} = $props();

	const dashboard = new SecurityDashboardState();
	let nodes = $state.raw<NodeSummary[]>([]);
	let nodesLoaded = $state(false);
	let loadedAt = $state(Date.now());
	let exported = $state('');

	$effect(() => {
		const ctrl = new AbortController();
		api.fleet
			.getFleetSummary({}, { signal: ctrl.signal })
			.then((res) => {
				nodes = res.nodes;
				nodesLoaded = true;
			})
			.catch((error: unknown) => {
				if (codeOf(error) !== Code.Canceled) nodesLoaded = true;
			});
		return () => ctrl.abort();
	});

	function load() {
		loadedAt = Date.now();
		exported = '';
		void dashboard.load(filter, fixedNode, loadedAt);
	}

	$effect(() => {
		void [filter, fixedNode];
		untrack(load);
	});
	$effect(() => () => dashboard.destroy());

	const locale = $derived(localeState.current);
	const names = $derived(new Map(nodes.map((n) => [n.nodeId, n.hostname])));
	const overview = $derived(dashboard.overview);
	const step = $derived(overview?.step ? durationMs(overview.step) : 0);
	const noNodes = $derived(nodesLoaded && nodes.length === 0 && !fixedNode);

	async function exportCSV() {
		exported = await dashboard.exportCSV();
	}

	const pickIP = (ip: string) => onFilter({ ...filter, network: ip, kinds: [...failedKinds] });
	const pickUser = (user: string) => onFilter({ ...filter, user, kinds: [...failedKinds] });
</script>

<div class="flex flex-col gap-6">
	<SecurityFilters {filter} {nodes} fixedNode={fixedNode !== undefined} onApply={onFilter} />

	{#if dashboard.error}
		<div class="flex flex-col items-start gap-2">
			<p role="alert" class="text-sm text-destructive">{dashboard.error}</p>
			<Button variant="outline" size="sm" onclick={load}>{m.retry()}</Button>
		</div>
	{/if}

	{#if noNodes}
		<div class="flex flex-col items-start gap-1 rounded-md border border-dashed border-border p-4">
			<p class="text-sm">{m.secdash_no_nodes()}</p>
			{#if (sessionState.current?.enrollGroupIds.length ?? 0) > 0}
				<Button href={resolve('/(app)/nodes/add')} variant="link" size="sm" class="px-0">
					{m.addnode_title()}
				</Button>
			{/if}
		</div>
	{/if}

	<SecurityTotals totals={overview?.totals} {filter} {onFilter} />

	<FailedLoginsChart
		series={overview?.failedLogins ?? []}
		{step}
		range={dashboard.range}
		{names}
		loading={dashboard.overviewStatus === 'loading'}
		failed={dashboard.overviewStatus === 'error'}
		onZoom={(from, to) => onFilter({ ...filter, from, to })}
	/>

	<div class="grid gap-4 lg:grid-cols-2">
		<TopList
			title={m.secdash_top_ips()}
			topic="figures/failed_logins"
			items={overview?.topIps ?? []}
			now={new Date(loadedAt)}
			onPick={pickIP}
			pickLabel={(v) => m.secdash_pick_ip({ ip: v })}
		/>
		<TopList
			title={m.secdash_top_users()}
			topic="figures/failed_logins"
			items={overview?.topUsers ?? []}
			now={new Date(loadedAt)}
			onPick={pickUser}
			pickLabel={(v) => m.secdash_pick_user({ user: v })}
		/>
	</div>
	{#if overview?.geoipNotice}
		<p class="-mt-4 text-xs text-muted-foreground">
			{m.secdash_geoip_notice({ notice: overview.geoipNotice })}
		</p>
	{/if}

	<section class="flex flex-col gap-2" aria-labelledby="sec-events-title">
		<div class="flex flex-wrap items-center gap-2">
			<h2 id="sec-events-title" class="text-lg font-medium">{m.secdash_table_title()}</h2>
			{#if dashboard.eventsStatus === 'ready'}
				<span class="text-sm text-muted-foreground">
					{dashboard.next
						? m.secdash_count_more({ count: formatNumber(dashboard.events.length, locale) })
						: m.secdash_count({ count: formatNumber(dashboard.events.length, locale) })}
				</span>
			{/if}
			<div class="ml-auto flex flex-wrap gap-2">
				<Button
					variant="ghost"
					size="sm"
					onclick={load}
					disabled={dashboard.eventsStatus === 'loading'}
				>
					<RefreshCw aria-hidden="true" />
					{m.logs_refresh()}
				</Button>
				<Button
					variant="outline"
					size="sm"
					onclick={exportCSV}
					disabled={dashboard.exporting || dashboard.events.length === 0}
				>
					<Download aria-hidden="true" />
					{dashboard.exporting ? m.secdash_exporting() : m.secdash_export()}
				</Button>
			</div>
		</div>
		<p aria-live="polite" class="text-sm text-muted-foreground">{exported}</p>

		{#if dashboard.eventsStatus === 'loading' && dashboard.events.length === 0}
			<p role="status" class="text-sm text-muted-foreground">{m.loading()}</p>
		{:else if dashboard.eventsStatus === 'ready' && dashboard.events.length === 0}
			<div
				class="flex flex-col items-start gap-1 rounded-md border border-dashed border-border p-4"
			>
				<p class="text-sm">{m.secdash_empty()}</p>
				<p class="text-sm text-muted-foreground">{m.secdash_empty_hint()}</p>
			</div>
		{:else if dashboard.events.length > 0}
			<SecurityEventTable
				events={dashboard.events}
				{names}
				now={loadedAt}
				showNode={fixedNode === undefined}
			/>
			{#if dashboard.next}
				<Button
					variant="outline"
					size="sm"
					class="self-start"
					disabled={dashboard.loadingMore}
					onclick={() => dashboard.loadMore()}
				>
					{dashboard.loadingMore ? m.loading() : m.secdash_load_more()}
				</Button>
			{/if}
		{/if}
	</section>
</div>
