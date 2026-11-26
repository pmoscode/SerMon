<script lang="ts">
	import { displayText } from '#lib/logs/text.js';
	import ShieldAlert from '@lucide/svelte/icons/shield-alert';
	import { timestampDate } from '@bufbuild/protobuf/wkt';
	import type { Node } from '#lib/api/gen/sermon/web/v1/node_pb.js';
	import Meter from '#lib/components/fleet/Meter.svelte';
	import StatusBadge from '#lib/components/fleet/StatusBadge.svelte';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import { assess } from '#lib/fleet/status.js';
	import {
		formatAgo,
		formatBytes,
		formatDuration,
		formatTimestamp,
		formatValue
	} from '#lib/format/format.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { m } from '#lib/paraglide/messages.js';

	/** Header of the node detail: host facts and key figures with help (KONZEPT §8.2, §16). */
	let { node, now }: { node: Node; now: Date } = $props();
	const locale = $derived(localeState.current);
	const f = $derived(node.figures);
</script>

<section class="flex flex-col gap-4 rounded-lg border bg-card p-4" aria-labelledby="node-title">
	<div class="flex flex-wrap items-start justify-between gap-2">
		<div>
			<h1 id="node-title" class="text-2xl font-semibold tracking-tight">
				{displayText(node.hostname)}
			</h1>
			<p class="text-sm text-muted-foreground">
				{node.groupName} · {node.osId}
				{node.osVersion} · {m.node_kernel()}
				{node.kernelRelease} · {node.architecture}
			</p>
		</div>
		<div class="flex flex-wrap items-center gap-3">
			{#if node.lockedAt}
				<!-- A lock refuses the agent's certificate: visible at once, details in the tab. -->
				<span
					class="inline-flex items-center gap-1 text-sm font-medium text-red-700 dark:text-red-400"
				>
					<ShieldAlert class="size-4" aria-hidden="true" />
					{m.node_locked_badge()}
				</span>
			{/if}
			{#if node.deletionDueAt}
				<!-- M5-29: named in text; the icon stays out of the page's first load (budget). -->
				<span class="text-sm font-medium text-red-700 dark:text-red-400">
					{m.node_deletion_badge({
						date: formatTimestamp(Number(node.deletionDueAt.seconds), locale)
					})}
				</span>
			{/if}
			<StatusBadge
				status={assess({
					online: node.online,
					figures: node.figures,
					deactivated: node.deactivatedAt !== undefined
				}).status}
				class="text-sm"
			/>
		</div>
	</div>
	<dl class="grid grid-cols-2 gap-x-6 gap-y-2 text-sm sm:grid-cols-3 lg:grid-cols-6">
		<div>
			<dt class="text-xs text-muted-foreground">{m.node_cpus()}</dt>
			<dd class="font-mono tabular-nums">{node.cpuCount}</dd>
		</div>
		<div>
			<dt class="text-xs text-muted-foreground">{m.node_memory()}</dt>
			<dd class="font-mono tabular-nums">{formatBytes(Number(node.memoryBytes), locale)}</dd>
		</div>
		<div>
			<dt class="flex items-center gap-0.5 text-xs text-muted-foreground">
				{m.figure_uptime()}<HelpTip topic="figures/uptime" />
			</dt>
			<dd>{f?.uptimeSeconds === undefined ? '–' : formatDuration(f.uptimeSeconds, locale)}</dd>
		</div>
		<div>
			<dt class="text-xs text-muted-foreground">{m.node_agent()}</dt>
			<dd class="font-mono">{node.agentVersion || '–'}</dd>
		</div>
		<div>
			<dt class="text-xs text-muted-foreground">{m.node_enrolled()}</dt>
			<dd>
				{node.enrolledAt
					? formatTimestamp(timestampDate(node.enrolledAt).getTime() / 1000, locale)
					: '–'}
			</dd>
		</div>
		<div>
			<dt class="text-xs text-muted-foreground">{m.node_last_contact()}</dt>
			<dd>
				{node.lastSeenAt
					? formatAgo(timestampDate(node.lastSeenAt), now, locale)
					: m.node_never_seen()}
			</dd>
		</div>
	</dl>
	{#if node.online}
		<div class="grid grid-cols-2 gap-4 sm:grid-cols-4">
			<Meter label={m.figure_cpu()} value={f?.cpuBusyRatio} topic="figures/cpu_busy" />
			<Meter label={m.figure_memory()} value={f?.memoryUsedRatio} topic="figures/memory_used" />
			<Meter
				label={m.figure_disk()}
				value={f?.filesystemMaxUsedRatio}
				topic="figures/filesystem_max_used"
			/>
			<div class="flex flex-col gap-1 text-xs">
				<span class="flex items-center gap-0.5 text-muted-foreground">
					{m.figure_load()}<HelpTip topic="figures/load_per_cpu" />
				</span>
				<span class="font-mono text-sm tabular-nums">
					{f?.loadPerCpu === undefined ? '–' : formatValue('load', f.loadPerCpu, locale)}
				</span>
			</div>
		</div>
	{/if}
</section>
