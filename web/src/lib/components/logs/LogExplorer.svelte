<script lang="ts">
	import Pause from '@lucide/svelte/icons/pause';
	import Play from '@lucide/svelte/icons/play';
	import Radio from '@lucide/svelte/icons/radio';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import Square from '@lucide/svelte/icons/square';
	import { untrack } from 'svelte';
	import { resolve } from '$app/paths';
	import { api, Code, codeOf } from '#lib/api/client.js';
	import type { LogLine } from '#lib/api/gen/sermon/web/v1/log_pb.js';
	import type { NodeSummary } from '#lib/api/gen/sermon/web/v1/fleet_pb.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { formatNumber } from '#lib/format/format.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { LogExplorerState } from '#lib/logs/explorer.svelte.js';
	import { lineKey, MAX_LINES } from '#lib/logs/lines.js';
	import { rangeOf, type LogFilter } from '#lib/logs/filter.js';
	import { selectedNodes } from '#lib/logs/notices.js';
	import { m } from '#lib/paraglide/messages.js';
	import { sessionState } from '#lib/stores/session.svelte.js';
	import LogContextDialog from './LogContextDialog.svelte';
	import LogFilters from './LogFilters.svelte';
	import LogHistogram from './LogHistogram.svelte';
	import LogLineRow from './LogLineRow.svelte';
	import LogNotices from './LogNotices.svelte';
	import VirtualList from './VirtualList.svelte';

	/**
	 * The log explorer (KONZEPT §10.4): filters, histogram, the lines with their context, and the
	 * live tail with pause. The filter belongs to the caller: the log page keeps it in the URL, the
	 * node page in its tab.
	 */
	let {
		filter,
		onFilter,
		fixedNode
	}: {
		filter: LogFilter;
		onFilter: (filter: LogFilter) => void;
		/** On a node page: the node, without node and group choice. */
		fixedNode?: string;
	} = $props();

	const explorer = new LogExplorerState();
	let nodes = $state.raw<NodeSummary[]>([]);
	let nodesLoaded = $state(false);
	let contextLine = $state<LogLine | undefined>(undefined);
	let list = $state<VirtualList<LogLine>>();
	let searchedAt = $state(Date.now());

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

	function search() {
		searchedAt = Date.now();
		list?.scrollToTop();
		void explorer.search(filter, fixedNode, searchedAt);
	}

	// Only a new filter searches again; what the search reads (the tail's state) must not.
	$effect(() => {
		void [filter, fixedNode];
		untrack(search);
	});
	$effect(() => () => explorer.destroy());

	const range = $derived(rangeOf(filter, searchedAt));
	const selected = $derived(selectedNodes(nodes, filter, fixedNode));
	const noNodes = $derived(nodesLoaded && nodes.length === 0);
	const count = $derived(formatNumber(explorer.lines.length, localeState.current));

	function toggleLive() {
		if (explorer.live === 'off') {
			list?.scrollToTop();
			explorer.startTail();
		} else {
			explorer.stopTail();
		}
	}

	const liveText = $derived.by(() => {
		if (explorer.live === 'off') return '';
		if (explorer.paused)
			return m.logs_live_paused({ count: formatNumber(explorer.pending, localeState.current) });
		switch (explorer.live) {
			case 'connecting':
				return m.logs_live_connecting();
			case 'reconnecting':
				return m.logs_live_reconnecting();
			default:
				return m.logs_live_on();
		}
	});
</script>

<div class="flex flex-col gap-4">
	<LogFilters {filter} {nodes} fixedNode={fixedNode !== undefined} onApply={onFilter} />

	<LogNotices nodes={selected} {range} />

	<LogHistogram
		data={explorer.histogram}
		loading={explorer.status === 'loading' && !explorer.histogram}
		failed={explorer.histogramFailed}
		onZoom={(from, to) => onFilter({ ...filter, from, to })}
	/>

	<div class="flex flex-col gap-2">
		<div class="flex flex-wrap items-center gap-2">
			<h2 class="text-lg font-medium">{m.logs_lines_title()}</h2>
			{#if explorer.status === 'ready'}
				<span class="text-sm text-muted-foreground">
					{explorer.more ? m.logs_lines_count_more({ count }) : m.logs_lines_count({ count })}
				</span>
			{/if}
			<div class="ml-auto flex flex-wrap gap-2">
				<Button variant="ghost" size="sm" onclick={search} disabled={explorer.status === 'loading'}>
					<RefreshCw aria-hidden="true" />
					{m.logs_refresh()}
				</Button>
				{#if explorer.live !== 'off'}
					<Button
						variant="outline"
						size="sm"
						onclick={() => (explorer.paused ? explorer.resume() : explorer.pause())}
					>
						{#if explorer.paused}<Play aria-hidden="true" />{m.logs_live_resume()}{:else}<Pause
								aria-hidden="true"
							/>{m.logs_live_pause()}{/if}
					</Button>
				{/if}
				<Button
					variant={explorer.live === 'off' ? 'outline' : 'secondary'}
					size="sm"
					aria-pressed={explorer.live !== 'off'}
					onclick={toggleLive}
				>
					{#if explorer.live === 'off'}<Radio
							aria-hidden="true"
						/>{m.logs_live_start()}{:else}<Square aria-hidden="true" />{m.logs_live_stop()}{/if}
				</Button>
			</div>
		</div>

		<div aria-live="polite" class="flex flex-col gap-1 text-sm">
			{#if liveText}<p class="text-muted-foreground">{liveText}</p>{/if}
			{#if explorer.live !== 'off' && explorer.throttled}
				<p class="text-amber-700 dark:text-amber-400">{m.logs_live_throttled()}</p>
			{/if}
			{#if explorer.live !== 'off' && explorer.storeDown}
				<p class="text-amber-700 dark:text-amber-400">{m.logs_error_unavailable()}</p>
			{/if}
		</div>
		{#if explorer.liveError}
			<p role="alert" class="text-sm text-destructive">{explorer.liveError}</p>
		{/if}

		{#if explorer.status === 'loading' && explorer.lines.length === 0}
			<p role="status" class="text-sm text-muted-foreground">{m.loading()}</p>
		{:else if explorer.status === 'error'}
			<div class="flex flex-col items-start gap-2">
				<p role="alert" class="text-sm text-destructive">{explorer.error}</p>
				<Button variant="outline" size="sm" onclick={search}>{m.retry()}</Button>
			</div>
		{:else if explorer.lines.length === 0}
			<div
				class="flex flex-col items-start gap-1 rounded-md border border-dashed border-border p-4"
			>
				{#if noNodes && !fixedNode}
					<p class="text-sm">{m.logs_empty_no_nodes()}</p>
					{#if (sessionState.current?.enrollGroupIds.length ?? 0) > 0}
						<Button href={resolve('/(app)/nodes/add')} variant="link" size="sm" class="px-0">
							{m.addnode_title()}
						</Button>
					{/if}
				{:else}
					<p class="text-sm">{m.logs_empty()}</p>
					<p class="text-sm text-muted-foreground">{m.logs_empty_hint()}</p>
				{/if}
			</div>
		{:else}
			<VirtualList
				bind:this={list}
				items={explorer.lines}
				key={lineKey}
				label={m.logs_lines_title()}
				class="rounded-md border border-border {explorer.status === 'loading' ? 'opacity-60' : ''}"
			>
				{#snippet row(line: LogLine)}
					<LogLineRow
						{line}
						showHost={fixedNode === undefined}
						onContext={(l) => (contextLine = l)}
					/>
				{/snippet}
			</VirtualList>
			{#if explorer.more}
				<Button
					variant="outline"
					size="sm"
					class="self-start"
					disabled={explorer.loadingMore}
					onclick={() => explorer.loadMore()}
				>
					{explorer.loadingMore ? m.loading() : m.logs_load_more()}
				</Button>
			{:else if explorer.lines.length >= MAX_LINES}
				<p class="text-sm text-muted-foreground">{m.logs_lines_limit({ count })}</p>
			{/if}
		{/if}
	</div>
</div>

<LogContextDialog
	line={contextLine}
	load={(l) => explorer.context(l)}
	onClose={() => (contextLine = undefined)}
/>
