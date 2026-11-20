<script lang="ts">
	import Lock from '@lucide/svelte/icons/lock';
	import { timestampDate } from '@bufbuild/protobuf/wkt';
	import type { Filesystem } from '#lib/api/gen/sermon/web/v1/node_pb.js';
	import Meter from '#lib/components/fleet/Meter.svelte';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import { formatIn } from '#lib/format/forecast.js';
	import { formatBytes, formatPercent } from '#lib/format/format.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { m } from '#lib/paraglide/messages.js';

	/** Mount points with fill level, inodes and the forecast when they run full (KONZEPT §8.2). */
	let { filesystems, now }: { filesystems: Filesystem[]; now: Date } = $props();
	const locale = $derived(localeState.current);
	const inodes = (fs: Filesystem) =>
		fs.inodesTotal > 0n ? Number(fs.inodesUsed) / Number(fs.inodesTotal) : undefined;
</script>

<div class="overflow-x-auto rounded-lg border">
	<table class="w-full text-sm">
		<caption class="sr-only">{m.node_filesystems()}</caption>
		<thead class="bg-muted/50 text-left text-xs text-muted-foreground">
			<tr>
				<th scope="col" class="px-3 py-2 font-medium">{m.node_fs_mountpoint()}</th>
				<th scope="col" class="w-48 px-3 py-2 font-medium">
					<span class="inline-flex items-center gap-0.5"
						>{m.node_fs_used()}<HelpTip topic="metrics/sermon_filesystem_utilization_ratio" /></span
					>
				</th>
				<th scope="col" class="px-3 py-2 text-right font-medium">{m.node_fs_size()}</th>
				<th scope="col" class="px-3 py-2 text-right font-medium">{m.node_fs_avail()}</th>
				<th scope="col" class="px-3 py-2 text-right font-medium">
					<span class="inline-flex items-center gap-0.5"
						>{m.node_fs_inodes()}<HelpTip topic="metrics/sermon_filesystem_inodes_used" /></span
					>
				</th>
				<th scope="col" class="px-3 py-2 font-medium">
					<span class="inline-flex items-center gap-0.5"
						>{m.node_fs_forecast()}<HelpTip topic="metrics/sermon_filesystem_avail_bytes" /></span
					>
				</th>
			</tr>
		</thead>
		<tbody>
			{#each filesystems as fs (fs.mountpoint)}
				{@const inodeShare = inodes(fs)}
				<tr class="border-t">
					<th scope="row" class="px-3 py-2 text-left font-mono font-normal">
						{fs.mountpoint}
						{#if fs.readonly}
							<span
								class="ml-1 inline-flex items-center gap-0.5 font-sans text-xs text-amber-700 dark:text-amber-400"
							>
								<Lock class="size-3" aria-hidden="true" />{m.node_fs_readonly()}
							</span>
						{/if}
					</th>
					<td class="px-3 py-2"><Meter label={m.node_fs_used()} value={fs.usedRatio} compact /></td>
					<td class="px-3 py-2 text-right font-mono tabular-nums"
						>{formatBytes(Number(fs.sizeBytes), locale)}</td
					>
					<td class="px-3 py-2 text-right font-mono tabular-nums"
						>{formatBytes(Number(fs.availBytes), locale)}</td
					>
					<td class="px-3 py-2 text-right font-mono tabular-nums">
						{inodeShare === undefined ? '–' : formatPercent(inodeShare, locale)}
					</td>
					<td class="px-3 py-2">
						{#if fs.fullAt}
							<span class="text-amber-700 dark:text-amber-400">
								{m.node_fs_full_in({ when: formatIn(timestampDate(fs.fullAt), now, locale) })}
							</span>
						{:else}
							<span class="text-muted-foreground">{m.node_fs_no_forecast()}</span>
						{/if}
					</td>
				</tr>
			{/each}
		</tbody>
	</table>
</div>
