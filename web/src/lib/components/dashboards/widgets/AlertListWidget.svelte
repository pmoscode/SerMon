<script lang="ts">
	import { AlertState, type Alert } from '#lib/api/gen/sermon/web/v1/alert_pb.js';
	import AlertStateBadge from '#lib/components/alerts/AlertStateBadge.svelte';
	import { dimensionText, ruleName } from '#lib/alerts/view.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { displayText } from '#lib/logs/text.js';
	import { m } from '#lib/paraglide/messages.js';

	/** The active alerts, the most severe first; at most MAX_ALERTS. */
	const MAX_ALERTS = 100;
	let { title, alerts }: { title: string; alerts: Alert[] } = $props();

	const rank: Partial<Record<AlertState, number>> = {
		[AlertState.CRITICAL]: 0,
		[AlertState.WARNING]: 1,
		[AlertState.PENDING]: 2
	};
	const active = $derived(
		alerts
			.filter((a) => a.state in rank)
			.sort(
				(a, b) =>
					(rank[a.state] ?? 9) - (rank[b.state] ?? 9) || a.hostname.localeCompare(b.hostname)
			)
	);
</script>

{#if active.length === 0}
	<p class="text-sm text-muted-foreground">{m.widget_no_alerts()}</p>
{:else}
	<ul class="flex flex-col gap-1 text-sm" aria-label={title}>
		{#each active.slice(0, MAX_ALERTS) as a, i (i)}
			<li class="flex min-w-0 flex-wrap items-center gap-x-2">
				<AlertStateBadge state={a.state} />
				<span class="min-w-0 truncate font-medium"
					>{displayText(ruleName(a, localeState.current))}</span
				>
				<span class="min-w-0 truncate text-muted-foreground">
					{displayText(a.hostname)}{#if dimensionText(a)}
						· {displayText(dimensionText(a))}{/if}
				</span>
			</li>
		{/each}
	</ul>
	{#if active.length > MAX_ALERTS}
		<p class="text-xs text-muted-foreground">
			{m.widget_more_alerts({ count: active.length - MAX_ALERTS })}
		</p>
	{/if}
{/if}
