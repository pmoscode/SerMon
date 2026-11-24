<script lang="ts">
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import { timestampDate } from '@bufbuild/protobuf/wkt';
	import type { SecurityEventCount } from '#lib/api/gen/sermon/web/v1/security_event_pb.js';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import { formatAgo, formatNumber } from '#lib/format/format.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { displayText } from '#lib/logs/text.js';
	import { m } from '#lib/paraglide/messages.js';

	/**
	 * The addresses or user names with the most failed logins. Values come from the hosts' logs
	 * (an attacker chooses user names): text only, invisible characters made visible.
	 */
	let {
		title,
		topic,
		items,
		now,
		onPick,
		pickLabel
	}: {
		title: string;
		topic: string;
		items: SecurityEventCount[];
		now: Date;
		onPick: (value: string) => void;
		/** The accessible name of the filter button of a value. */
		pickLabel: (value: string) => string;
	} = $props();

	const locale = $derived(localeState.current);
	const top = $derived(Math.max(1, ...items.map((i) => Number(i.count))));
	const shown = (v: string) => (v === '' ? m.secdash_empty_user() : displayText(v));
	const id = $props.id();
</script>

<section class="flex flex-col gap-2 rounded-lg border bg-card p-3" aria-labelledby="{id}-title">
	<h2 id="{id}-title" class="flex items-center gap-1 text-sm font-medium">
		{title}<HelpTip {topic} />
	</h2>
	{#if items.length === 0}
		<p class="text-sm text-muted-foreground">{m.secdash_top_empty()}</p>
	{:else}
		<ol class="flex flex-col gap-1.5">
			{#each items as item (item.value)}
				<li class="flex flex-col gap-0.5">
					<div class="flex items-center gap-2 text-sm">
						<button
							type="button"
							class="min-w-0 truncate text-left font-mono underline-offset-2 hover:underline focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none {item.value ===
							''
								? 'text-muted-foreground italic'
								: ''}"
							aria-label={pickLabel(shown(item.value))}
							onclick={() => onPick(item.value)}>{shown(item.value)}</button
						>
						{#if item.unverified > 0n}
							<span
								class="inline-flex shrink-0 items-center gap-0.5 text-xs text-amber-700 dark:text-amber-400"
							>
								<TriangleAlert class="size-3" aria-hidden="true" />
								{m.secdash_unverified_count({
									count: formatNumber(Number(item.unverified), locale)
								})}
							</span>
						{/if}
						<span class="ml-auto shrink-0 font-medium tabular-nums">
							{formatNumber(Number(item.count), locale)}
						</span>
					</div>
					<div class="h-1 rounded-full bg-muted" aria-hidden="true">
						<div
							class="h-1 rounded-full bg-primary/60"
							style:width="{(Number(item.count) / top) * 100}%"
						></div>
					</div>
					<p class="text-xs text-muted-foreground">
						{m.secdash_top_meta({
							nodes: formatNumber(item.nodes, locale),
							ago: item.lastAt ? formatAgo(timestampDate(item.lastAt), now, locale) : '–'
						})}
					</p>
				</li>
			{/each}
		</ol>
	{/if}
</section>
