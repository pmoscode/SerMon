<script lang="ts">
	import { timestampDate } from '@bufbuild/protobuf/wkt';
	import { resolve } from '$app/paths';
	import type { NodeSummary } from '#lib/api/gen/sermon/web/v1/fleet_pb.js';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import { assess } from '#lib/fleet/status.js';
	import { formatAgo, formatDuration, formatPercent, formatValue } from '#lib/format/format.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { m } from '#lib/paraglide/messages.js';
	import Sparkline from './Sparkline.svelte';
	import StatusBadge from './StatusBadge.svelte';
	import TagList from './TagList.svelte';
	import UpdateBadges from './UpdateBadges.svelte';

	let {
		nodes,
		sparklines,
		now
	}: { nodes: NodeSummary[]; sparklines: Record<string, number[]>; now: Date } = $props();
	const locale = $derived(localeState.current);
	const percent = (v: number | undefined) => (v === undefined ? '–' : formatPercent(v, locale));
</script>

<div class="overflow-x-auto rounded-lg border">
	<table class="w-full text-sm">
		<caption class="sr-only">{m.fleet_table_caption()}</caption>
		<thead class="bg-muted/50 text-left text-xs text-muted-foreground">
			<tr>
				<th scope="col" class="px-3 py-2 font-medium">{m.fleet_col_node()}</th>
				<th scope="col" class="px-3 py-2 font-medium">{m.fleet_col_status()}</th>
				<th scope="col" class="px-3 py-2 text-right font-medium">
					<span class="inline-flex items-center gap-0.5"
						>{m.figure_cpu()}<HelpTip topic="figures/cpu_busy" /></span
					>
				</th>
				<th scope="col" class="px-3 py-2 text-right font-medium">
					<span class="inline-flex items-center gap-0.5"
						>{m.figure_memory()}<HelpTip topic="figures/memory_used" /></span
					>
				</th>
				<th scope="col" class="px-3 py-2 text-right font-medium">
					<span class="inline-flex items-center gap-0.5"
						>{m.figure_disk()}<HelpTip topic="figures/filesystem_max_used" /></span
					>
				</th>
				<th scope="col" class="px-3 py-2 text-right font-medium">
					<span class="inline-flex items-center gap-0.5"
						>{m.figure_load()}<HelpTip topic="figures/load_per_cpu" /></span
					>
				</th>
				<th scope="col" class="px-3 py-2 text-right font-medium">
					<span class="inline-flex items-center gap-0.5"
						>{m.figure_network()}<HelpTip topic="figures/network" /></span
					>
				</th>
				<th scope="col" class="px-3 py-2 text-right font-medium">
					<span class="inline-flex items-center gap-0.5"
						>{m.figure_uptime()}<HelpTip topic="figures/uptime" /></span
					>
				</th>
				<th scope="col" class="px-3 py-2 font-medium"
					><span class="sr-only">{m.fleet_col_trend()}</span></th
				>
			</tr>
		</thead>
		<tbody>
			{#each nodes as node (node.nodeId)}
				{@const f = node.figures}
				<tr class="border-t">
					<th scope="row" class="px-3 py-2 text-left font-medium">
						<a href={resolve('/(app)/nodes/[id]', { id: node.nodeId })} class="hover:underline"
							>{node.hostname}</a
						>
						<span class="block text-xs font-normal text-muted-foreground">{node.groupName}</span>
						<TagList tags={node.tags} class="mt-1 font-normal" />
						<UpdateBadges {node} class="mt-1 font-normal" />
					</th>
					<td class="px-3 py-2"><StatusBadge status={assess(node).status} /></td>
					{#if node.online}
						<td class="px-3 py-2 text-right font-mono tabular-nums">{percent(f?.cpuBusyRatio)}</td>
						<td class="px-3 py-2 text-right font-mono tabular-nums"
							>{percent(f?.memoryUsedRatio)}</td
						>
						<td class="px-3 py-2 text-right font-mono tabular-nums"
							>{percent(f?.filesystemMaxUsedRatio)}</td
						>
						<td class="px-3 py-2 text-right font-mono tabular-nums">
							{f?.loadPerCpu === undefined ? '–' : formatValue('load', f.loadPerCpu, locale)}
						</td>
						<td class="px-3 py-2 text-right font-mono text-xs tabular-nums">
							{f?.networkReceiveBytesPerSecond === undefined
								? '–'
								: `↓ ${formatValue('bytes_per_second', f.networkReceiveBytesPerSecond, locale)}`}
						</td>
						<td class="px-3 py-2 text-right">
							{f?.uptimeSeconds === undefined ? '–' : formatDuration(f.uptimeSeconds, locale)}
						</td>
						<td class="w-24 px-3 py-2"
							><Sparkline values={sparklines[node.nodeId] ?? []} height={20} /></td
						>
					{:else}
						<td colspan="7" class="px-3 py-2 text-muted-foreground">
							{node.lastSeenAt
								? m.node_last_seen({ ago: formatAgo(timestampDate(node.lastSeenAt), now, locale) })
								: m.node_never_seen()}
						</td>
					{/if}
				</tr>
			{/each}
		</tbody>
	</table>
</div>
