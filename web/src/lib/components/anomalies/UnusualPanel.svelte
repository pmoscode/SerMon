<script lang="ts">
	import SearchCheck from '@lucide/svelte/icons/search-check';
	import { timestampFromMs, timestampMs } from '@bufbuild/protobuf/wkt';
	import { resolve } from '$app/paths';
	import { bandRange } from '#lib/anomalies/band.js';
	import { anomaliesApi } from '#lib/api/anomalies.js';
	import type { UnusualSeries } from '#lib/api/gen/sermon/web/v1/anomaly_pb.js';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import { displayText } from '#lib/logs/text.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { m } from '#lib/paraglide/messages.js';
	import { helpState } from '#lib/stores/help.svelte.js';
	import type { Range } from '#lib/stores/timerange.svelte.js';

	/**
	 * "What was unusual?" (KONZEPT §13, M4-25): the series with the highest scores in a range, of
	 * one node or (without nodeId) of the fleet. Loaded on request: the range is the one marked
	 * by zooming into a chart.
	 */
	let {
		range,
		nodeId,
		hostnames = {},
		auto = false
	}: {
		range: Range;
		nodeId?: string;
		/** Host names of the fleet's nodes, for a list without nodeId. */
		hostnames?: Record<string, string>;
		/** Load at once (the dashboard) instead of on the button. */
		auto?: boolean;
	} = $props();

	let found = $state<UnusualSeries[] | undefined>();
	let loading = $state(false);
	let error = $state('');
	let outOfRange = $state(false);

	const dateTime = $derived(
		new Intl.DateTimeFormat(localeState.current, { dateStyle: 'short', timeStyle: 'short' })
	);
	const score = $derived(new Intl.NumberFormat(localeState.current, { maximumFractionDigits: 1 }));

	async function load() {
		const shown = bandRange(range, Date.now());
		outOfRange = !shown;
		if (!shown) {
			found = [];
			return;
		}
		loading = true;
		try {
			const res = await anomaliesApi.listUnusual({
				nodeId: nodeId ?? '',
				from: timestampFromMs(shown.from),
				to: timestampFromMs(shown.to),
				limit: 20
			});
			found = res.series;
			error = '';
		} catch {
			error = m.error_unavailable();
		} finally {
			loading = false;
		}
	}

	$effect(() => {
		void [range.from, range.to];
		if (auto) void load();
	});

	function metricName(metric: string): string {
		return helpState.get('metrics/' + metric)?.title ?? metric;
	}

	function labelsText(labels: Record<string, string>): string {
		return Object.entries(labels)
			.sort(([a], [b]) => a.localeCompare(b))
			.map(([k, v]) => `${k}=${displayText(v)}`)
			.join(', ');
	}
</script>

<section class="flex flex-col gap-2" aria-labelledby="unusual-title">
	<div class="flex flex-wrap items-center gap-2">
		<h2 id="unusual-title" class="text-sm font-medium">{m.unusual_title()}</h2>
		<HelpTip topic="concepts/anomalies" />
		{#if !auto}
			<Button variant="outline" size="sm" onclick={load} disabled={loading}>
				<SearchCheck aria-hidden="true" />
				{m.unusual_check()}
			</Button>
		{/if}
	</div>
	{#if loading}
		<p role="status" class="text-sm text-muted-foreground">{m.loading()}</p>
	{:else if error}
		<p role="alert" class="text-sm text-destructive">{error}</p>
	{:else if !auto && found === undefined}
		<p class="text-sm text-muted-foreground">{m.unusual_hint()}</p>
	{:else if outOfRange}
		<p class="text-sm text-muted-foreground">{m.unusual_too_old()}</p>
	{:else if found && found.length === 0}
		<p class="text-sm text-muted-foreground">{m.unusual_none()}</p>
	{:else if found}
		<table class="w-full text-sm">
			<caption class="sr-only">{m.unusual_title()}</caption>
			<thead class="text-left text-xs text-muted-foreground">
				<tr>
					{#if !nodeId}<th scope="col" class="py-1 pr-3 font-normal">{m.unusual_node()}</th>{/if}
					<th scope="col" class="py-1 pr-3 font-normal">{m.unusual_metric()}</th>
					<th scope="col" class="py-1 pr-3 font-normal">{m.unusual_score()}</th>
					<th scope="col" class="py-1 pr-3 font-normal">{m.unusual_minutes()}</th>
					<th scope="col" class="py-1 font-normal">{m.unusual_peak()}</th>
				</tr>
			</thead>
			<tbody class="divide-y">
				<!-- By position: labels a host chose may look alike once made visible (review M4-30). -->
				{#each found as s, i (i)}
					<tr>
						{#if !nodeId}
							<td class="py-1 pr-3">
								<a
									class="underline-offset-2 hover:underline"
									href={resolve('/(app)/nodes/[id]', { id: s.nodeId })}
								>
									{displayText(hostnames[s.nodeId] ?? s.nodeId)}
								</a>
							</td>
						{/if}
						<td class="py-1 pr-3">
							{metricName(s.metric)}
							{#if Object.keys(s.labels).length > 0}
								<span class="text-xs text-muted-foreground">{labelsText(s.labels)}</span>
							{/if}
						</td>
						<td class="py-1 pr-3 tabular-nums">{score.format(s.maxScore)}</td>
						<td class="py-1 pr-3 tabular-nums">{s.anomalousMinutes}</td>
						<td class="py-1 tabular-nums">{s.peak ? dateTime.format(timestampMs(s.peak)) : '–'}</td>
					</tr>
				{/each}
			</tbody>
		</table>
	{/if}
</section>
