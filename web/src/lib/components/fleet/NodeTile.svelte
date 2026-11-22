<script lang="ts">
	import ArrowDown from '@lucide/svelte/icons/arrow-down';
	import ArrowUp from '@lucide/svelte/icons/arrow-up';
	import { timestampDate } from '@bufbuild/protobuf/wkt';
	import { resolve } from '$app/paths';
	import type { NodeSummary } from '#lib/api/gen/sermon/web/v1/fleet_pb.js';
	import { assess } from '#lib/fleet/status.js';
	import { formatAgo, formatDuration, formatValue } from '#lib/format/format.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { m } from '#lib/paraglide/messages.js';
	import Meter from './Meter.svelte';
	import Sparkline from './Sparkline.svelte';
	import StatusBadge from './StatusBadge.svelte';
	import TagList from './TagList.svelte';

	let {
		node,
		sparkline = [],
		now
	}: { node: NodeSummary; sparkline?: number[]; now: Date } = $props();

	const assessment = $derived(assess(node));
	const f = $derived(node.figures);
	const locale = $derived(localeState.current);
</script>

<article
	class="flex flex-col gap-3 rounded-lg border bg-card p-4 data-[status=critical]:border-red-600/60 data-[status=warning]:border-amber-500/60"
	data-status={assessment.status}
	aria-labelledby="node-{node.nodeId}"
>
	<header class="flex items-start justify-between gap-2">
		<div class="min-w-0">
			<h2 id="node-{node.nodeId}" class="truncate font-medium">
				<a href={resolve('/(app)/nodes/[id]', { id: node.nodeId })} class="hover:underline"
					>{node.hostname}</a
				>
			</h2>
			<p class="truncate text-xs text-muted-foreground">
				{node.groupName} · {node.osId}
				{node.osVersion}
			</p>
			<TagList tags={node.tags} class="mt-1" />
		</div>
		<StatusBadge status={assessment.status} class="shrink-0" />
	</header>
	{#if node.online}
		<div class="grid grid-cols-3 gap-3">
			<Meter label={m.figure_cpu()} value={f?.cpuBusyRatio} />
			<Meter label={m.figure_memory()} value={f?.memoryUsedRatio} />
			<Meter label={m.figure_disk()} value={f?.filesystemMaxUsedRatio} />
		</div>
		{#if sparkline.length > 1}
			<div class="flex flex-col gap-0.5">
				<span class="text-xs text-muted-foreground">{m.fleet_col_trend()}</span>
				<Sparkline values={sparkline} />
			</div>
		{/if}
		<div
			class="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 text-xs text-muted-foreground"
		>
			<span
				>{m.figure_uptime()}: {f?.uptimeSeconds === undefined
					? m.value_missing()
					: formatDuration(f.uptimeSeconds, locale)}</span
			>
			<span class="flex items-center gap-2 font-mono tabular-nums">
				<span class="inline-flex items-center gap-0.5">
					<ArrowDown class="size-3" aria-hidden="true" /><span class="sr-only"
						>{m.figure_receive()}</span
					>
					{f?.networkReceiveBytesPerSecond === undefined
						? '–'
						: formatValue('bytes_per_second', f.networkReceiveBytesPerSecond, locale)}
				</span>
				<span class="inline-flex items-center gap-0.5">
					<ArrowUp class="size-3" aria-hidden="true" /><span class="sr-only"
						>{m.figure_transmit()}</span
					>
					{f?.networkTransmitBytesPerSecond === undefined
						? '–'
						: formatValue('bytes_per_second', f.networkTransmitBytesPerSecond, locale)}
				</span>
			</span>
		</div>
	{:else}
		<p class="text-sm text-muted-foreground">
			{node.lastSeenAt
				? m.node_last_seen({ ago: formatAgo(timestampDate(node.lastSeenAt), now, locale) })
				: m.node_never_seen()}
		</p>
	{/if}
</article>
