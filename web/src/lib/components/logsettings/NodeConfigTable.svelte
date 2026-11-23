<script lang="ts">
	import { timestampDate } from '@bufbuild/protobuf/wkt';
	import CircleCheck from '@lucide/svelte/icons/circle-check';
	import EyeOff from '@lucide/svelte/icons/eye-off';
	import Hourglass from '@lucide/svelte/icons/hourglass';
	import TriangleAlert from '@lucide/svelte/icons/triangle-exclamation-point';
	import type { NodeConfigStatus } from '#lib/api/gen/sermon/web/v1/log_settings_pb.js';
	import LogText from '#lib/components/logs/LogText.svelte';
	import { formatAgo } from '#lib/format/format.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { m } from '#lib/paraglide/messages.js';

	/** What each agent reported about its configuration. Errors come from the host: text only. */
	let {
		nodes,
		version,
		groupNames,
		now
	}: {
		nodes: NodeConfigStatus[];
		version: bigint;
		groupNames: Map<string, string>;
		now: Date;
	} = $props();
</script>

<section class="flex flex-col gap-2" aria-labelledby="node-config-title">
	<h2 id="node-config-title" class="text-lg font-medium">{m.logsettings_nodes_title()}</h2>
	{#if nodes.length === 0}
		<p class="text-sm text-muted-foreground">{m.logsettings_nodes_none()}</p>
	{:else}
		<div class="overflow-x-auto rounded-md border border-border">
			<table class="w-full text-sm">
				<thead class="bg-muted/50 text-left text-xs text-muted-foreground">
					<tr>
						<th scope="col" class="px-3 py-2 font-medium">{m.logsettings_node()}</th>
						<th scope="col" class="px-3 py-2 font-medium">{m.logsettings_group()}</th>
						<th scope="col" class="px-3 py-2 font-medium">{m.logsettings_state()}</th>
						<th scope="col" class="px-3 py-2 font-medium">{m.logsettings_reported()}</th>
					</tr>
				</thead>
				<tbody>
					{#each nodes as n (n.nodeId)}
						<tr class="border-t border-border align-top">
							<td class="px-3 py-2 font-medium">{n.hostname}</td>
							<td class="px-3 py-2">{groupNames.get(n.groupId) ?? '–'}</td>
							<td class="flex flex-col gap-1 px-3 py-2">
								{#if n.error}
									<span class="inline-flex items-center gap-1 text-red-700 dark:text-red-400">
										<TriangleAlert class="size-3.5 shrink-0" aria-hidden="true" />
										{m.logsettings_state_error({ version: String(n.appliedVersion) })}
									</span>
									<span class="font-mono text-xs"><LogText text={n.error} /></span>
								{:else if n.appliedVersion === 0n}
									<span class="inline-flex items-center gap-1 text-muted-foreground">
										<Hourglass class="size-3.5 shrink-0" aria-hidden="true" />
										{m.logsettings_state_none()}
									</span>
								{:else if n.appliedVersion < version}
									<span class="inline-flex items-center gap-1 text-muted-foreground">
										<Hourglass class="size-3.5 shrink-0" aria-hidden="true" />
										{m.logsettings_state_pending({
											applied: String(n.appliedVersion),
											version: String(version)
										})}
									</span>
								{:else}
									<span class="inline-flex items-center gap-1">
										<CircleCheck
											class="size-3.5 shrink-0 text-emerald-700 dark:text-emerald-400"
											aria-hidden="true"
										/>
										{m.logsettings_state_current({ version: String(n.appliedVersion) })}
									</span>
								{/if}
								{#if n.logsVetoed}
									<span class="inline-flex items-center gap-1 text-muted-foreground">
										<EyeOff class="size-3.5 shrink-0" aria-hidden="true" />
										{m.logsettings_state_vetoed()}
									</span>
								{/if}
							</td>
							<td class="px-3 py-2 text-muted-foreground">
								{n.reportedAt
									? formatAgo(timestampDate(n.reportedAt), now, localeState.current)
									: '–'}
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/if}
</section>
