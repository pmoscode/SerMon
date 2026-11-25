<script lang="ts">
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import { resolve } from '$app/paths';
	import { fleetRate, loadRates, type NodeRate } from '#lib/anomalies/fleet.js';
	import UnusualPanel from '#lib/components/anomalies/UnusualPanel.svelte';
	import FleetNav from '#lib/components/fleet/FleetNav.svelte';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import { FleetState } from '#lib/fleet/fleet.svelte.js';
	import { displayText } from '#lib/logs/text.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { m } from '#lib/paraglide/messages.js';

	/** The anomalies dashboard (KONZEPT §16, 9; M4-25): nodes by anomaly rate, the fleet's rate and
	 * what was unusual in the last hour. */
	const REFRESH = 60_000;
	/** From here a node's rate warns (the default rule, M4-24). */
	const HIGH = 0.1;
	const UNUSUAL_SPAN = 3600_000;

	const fleet = new FleetState();
	let rates = $state<Record<string, NodeRate>>({});
	let status = $state<'loading' | 'ready' | 'error'>('loading');
	let now = $state(Date.now());

	async function refresh() {
		await fleet.load();
		if (fleet.status === 'error') {
			status = 'error';
			return;
		}
		try {
			now = Date.now();
			rates = await loadRates(
				(fleet.summary?.nodes ?? []).map((n) => n.nodeId),
				now
			);
			status = 'ready';
		} catch {
			status = 'error';
		}
	}

	$effect(() => {
		void refresh();
		const timer = setInterval(() => void refresh(), REFRESH);
		return () => clearInterval(timer);
	});

	const nodes = $derived(fleet.summary?.nodes ?? []);
	const hostnames = $derived(Object.fromEntries(nodes.map((n) => [n.nodeId, n.hostname])));
	const ranked = $derived(
		nodes
			.filter((n) => rates[n.nodeId])
			.map((n) => ({ node: n, rate: rates[n.nodeId] }))
			.sort((a, b) => b.rate.last - a.rate.last || b.rate.max - a.rate.max)
	);
	const total = $derived(fleetRate(rates));
	const percent = $derived(
		new Intl.NumberFormat(localeState.current, { style: 'percent', maximumFractionDigits: 1 })
	);
	const range = $derived({ from: now - UNUSUAL_SPAN, to: now });
</script>

<svelte:head>
	<title>{m.anomalies_title()} · SerMon</title>
</svelte:head>

<FleetNav />

<div class="flex flex-col gap-6">
	<div class="flex flex-wrap items-baseline gap-3">
		<h1 class="text-2xl font-semibold tracking-tight">{m.anomalies_title()}</h1>
		<HelpTip topic="metrics/sermon_anomaly_rate_ratio" />
		{#if total !== undefined}
			<p class="text-sm text-muted-foreground">
				{m.anomalies_fleet_rate({ rate: percent.format(total) })}
			</p>
		{/if}
	</div>

	{#if status === 'loading'}
		<p role="status" class="text-muted-foreground">{m.loading()}</p>
	{:else if status === 'error'}
		<div class="flex flex-col items-start gap-3">
			<p role="alert" class="text-destructive">{m.error_unavailable()}</p>
			<Button variant="outline" onclick={() => void refresh()}>{m.retry()}</Button>
		</div>
	{:else if ranked.length === 0}
		<p class="text-sm text-muted-foreground">{m.anomalies_none()}</p>
	{:else}
		<table class="w-full max-w-3xl text-sm">
			<caption class="sr-only">{m.anomalies_by_rate()}</caption>
			<thead class="text-left text-xs text-muted-foreground">
				<tr>
					<th scope="col" class="py-1 pr-3 font-normal">{m.unusual_node()}</th>
					<th scope="col" class="py-1 pr-3 font-normal">{m.anomalies_rate_now()}</th>
					<th scope="col" class="py-1 font-normal">{m.anomalies_rate_max()}</th>
				</tr>
			</thead>
			<tbody class="divide-y">
				{#each ranked as { node, rate } (node.nodeId)}
					<tr>
						<td class="py-1 pr-3">
							<a
								class="underline-offset-2 hover:underline"
								href={resolve('/(app)/nodes/[id]', { id: node.nodeId })}
							>
								{displayText(node.hostname)}
							</a>
						</td>
						<td class="py-1 pr-3 tabular-nums">
							{percent.format(rate.last)}
							{#if rate.last >= HIGH}
								<span class="ml-1 inline-flex items-center gap-1 text-xs text-destructive">
									<TriangleAlert class="size-3.5" aria-hidden="true" />
									{m.anomalies_high()}
								</span>
							{/if}
						</td>
						<td class="py-1 tabular-nums">{percent.format(rate.max)}</td>
					</tr>
				{/each}
			</tbody>
		</table>
	{/if}

	<UnusualPanel {range} {hostnames} auto />
</div>
