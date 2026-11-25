<script lang="ts">
	import Lock from '@lucide/svelte/icons/lock';
	import { timestampDate, timestampMs } from '@bufbuild/protobuf/wkt';
	import { resolve } from '$app/paths';
	import { capacityApi } from '#lib/api/capacity.js';
	import type { GetCapacityResponse } from '#lib/api/gen/sermon/web/v1/capacity_pb.js';
	import { topBy, trendOf, type NodeTrends, type TrendKey } from '#lib/capacity/trend.js';
	import FleetNav from '#lib/components/fleet/FleetNav.svelte';
	import Sparkline from '#lib/components/fleet/Sparkline.svelte';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import { formatIn } from '#lib/format/forecast.js';
	import { formatBytes, formatPercent } from '#lib/format/format.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { displayText } from '#lib/logs/text.js';
	import { m } from '#lib/paraglide/messages.js';

	/**
	 * Capacity planning (KONZEPT §16, 6; M4-26): the file systems of the fleet by when they run
	 * full, the CPU and memory trends of 30 days and the top consumers.
	 */
	const REFRESH = 5 * 60_000;
	/** Shown at first; the rest on request. */
	const FIRST = 20;

	let capacity = $state<GetCapacityResponse | undefined>();
	let status = $state<'loading' | 'ready' | 'error'>('loading');
	let now = $state(new Date());
	let allFilesystems = $state(false);
	let allNodes = $state(false);
	let sortBy = $state<TrendKey>('cpu');

	async function refresh() {
		try {
			capacity = await capacityApi.getCapacity({});
			now = new Date();
			status = 'ready';
		} catch {
			if (!capacity) status = 'error';
		}
	}

	$effect(() => {
		void refresh();
		const timer = setInterval(() => void refresh(), REFRESH);
		return () => clearInterval(timer);
	});

	const locale = $derived(localeState.current);
	const filesystems = $derived(capacity?.filesystems ?? []);
	const shownFilesystems = $derived(allFilesystems ? filesystems : filesystems.slice(0, FIRST));
	const sparklines = $derived(
		Object.fromEntries(
			(capacity?.trends ?? []).map((t) => [
				t.nodeId,
				{ cpu: t.cpu.map((d) => d.value), memory: t.memory.map((d) => d.value) }
			])
		)
	);
	const nodes = $derived<NodeTrends[]>(
		(capacity?.trends ?? []).map((t) => ({
			nodeId: t.nodeId,
			hostname: t.hostname,
			cpu: trendOf(t.cpu.map((d) => d.value)),
			memory: trendOf(t.memory.map((d) => d.value))
		}))
	);
	const ranked = $derived(topBy(nodes, sortBy));
	const shownNodes = $derived(allNodes ? ranked : ranked.slice(0, FIRST));
	const change = $derived(
		new Intl.NumberFormat(locale, {
			style: 'percent',
			maximumFractionDigits: 0,
			signDisplay: 'exceptZero'
		})
	);
	const period = $derived.by(() => {
		const days = capacity?.trends[0]?.cpu ?? [];
		const first = days[0]?.day;
		return first ? timestampMs(first) : undefined;
	});
</script>

<svelte:head>
	<title>{m.capacity_title()} · SerMon</title>
</svelte:head>

<FleetNav />

<div class="flex flex-col gap-8 pt-4">
	<div class="flex flex-wrap items-baseline gap-2">
		<h1 class="text-2xl font-semibold tracking-tight">{m.capacity_title()}</h1>
		<HelpTip topic="pages/capacity" />
	</div>

	{#if status === 'loading'}
		<p role="status" class="text-muted-foreground">{m.loading()}</p>
	{:else if status === 'error'}
		<div class="flex flex-col items-start gap-3">
			<p role="alert" class="text-destructive">{m.error_unavailable()}</p>
			<Button variant="outline" onclick={() => void refresh()}>{m.retry()}</Button>
		</div>
	{:else}
		<section class="flex flex-col gap-2" aria-labelledby="capacity-fs">
			<h2 id="capacity-fs" class="flex items-center gap-1 text-lg font-medium">
				{m.capacity_filesystems()}<HelpTip topic="metrics/sermon_filesystem_avail_bytes" />
			</h2>
			{#if filesystems.length === 0}
				<p class="text-sm text-muted-foreground">{m.capacity_no_filesystems()}</p>
			{:else}
				<div class="overflow-x-auto rounded-lg border">
					<table class="w-full text-sm">
						<caption class="sr-only">{m.capacity_filesystems()}</caption>
						<thead class="bg-muted/50 text-left text-xs text-muted-foreground">
							<tr>
								<th scope="col" class="px-3 py-2 font-medium">{m.unusual_node()}</th>
								<th scope="col" class="px-3 py-2 font-medium">{m.node_fs_mountpoint()}</th>
								<th scope="col" class="px-3 py-2 text-right font-medium">{m.node_fs_used()}</th>
								<th scope="col" class="px-3 py-2 text-right font-medium">{m.node_fs_avail()}</th>
								<th scope="col" class="px-3 py-2 text-right font-medium">{m.node_fs_size()}</th>
								<th scope="col" class="px-3 py-2 font-medium">{m.node_fs_forecast()}</th>
							</tr>
						</thead>
						<tbody>
							{#each shownFilesystems as fs (fs.nodeId + fs.mountpoint)}
								<tr class="border-t">
									<td class="px-3 py-2">
										<a
											class="underline-offset-2 hover:underline"
											href={resolve('/(app)/nodes/[id]', { id: fs.nodeId })}
										>
											{displayText(fs.hostname)}
										</a>
									</td>
									<th scope="row" class="px-3 py-2 text-left font-mono font-normal">
										{displayText(fs.mountpoint)}
										{#if fs.readonly}
											<span
												class="ml-1 inline-flex items-center gap-0.5 font-sans text-xs text-amber-700 dark:text-amber-400"
											>
												<Lock class="size-3" aria-hidden="true" />{m.node_fs_readonly()}
											</span>
										{/if}
									</th>
									<td class="px-3 py-2 text-right font-mono tabular-nums"
										>{formatPercent(fs.usedRatio, locale)}</td
									>
									<td class="px-3 py-2 text-right font-mono tabular-nums">
										{formatBytes(Number(fs.availBytes), locale)}
									</td>
									<td class="px-3 py-2 text-right font-mono tabular-nums">
										{formatBytes(Number(fs.sizeBytes), locale)}
									</td>
									<td class="px-3 py-2">
										{#if fs.fullAt}
											<span class="text-amber-700 dark:text-amber-400">
												{m.node_fs_full_in({
													when: formatIn(timestampDate(fs.fullAt), now, locale)
												})}
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
				{#if !allFilesystems && filesystems.length > FIRST}
					<Button
						variant="outline"
						size="sm"
						class="self-start"
						onclick={() => (allFilesystems = true)}
					>
						{m.capacity_show_all({ count: filesystems.length })}
					</Button>
				{/if}
				{#if capacity?.truncated}
					<p class="text-sm text-muted-foreground">
						{m.capacity_truncated({ count: filesystems.length })}
					</p>
				{/if}
			{/if}
		</section>

		<section class="flex flex-col gap-2" aria-labelledby="capacity-trends">
			<div class="flex flex-wrap items-center gap-2">
				<h2 id="capacity-trends" class="text-lg font-medium">{m.capacity_trends()}</h2>
				<div class="flex gap-1" role="group" aria-label={m.capacity_sort()}>
					{#each [['cpu', m.capacity_by_cpu()], ['memory', m.capacity_by_memory()]] as [key, label] (key)}
						<Button
							variant="outline"
							size="sm"
							aria-pressed={sortBy === key}
							class="aria-pressed:border-primary aria-pressed:ring-2 aria-pressed:ring-primary/30"
							onclick={() => (sortBy = key as TrendKey)}
						>
							{label}
						</Button>
					{/each}
				</div>
			</div>
			{#if nodes.length === 0}
				<p class="text-sm text-muted-foreground">{m.capacity_no_trends()}</p>
			{:else}
				<p class="text-sm text-muted-foreground">
					{m.capacity_trends_hint()}
					{#if period}
						{m.capacity_since({
							day: new Intl.DateTimeFormat(locale, { dateStyle: 'medium' }).format(period)
						})}
					{/if}
				</p>
				<div class="overflow-x-auto rounded-lg border">
					<table class="w-full text-sm">
						<caption class="sr-only">{m.capacity_top()}</caption>
						<thead class="bg-muted/50 text-left text-xs text-muted-foreground">
							<tr>
								<th scope="col" class="px-3 py-2 font-medium">{m.unusual_node()}</th>
								<th scope="col" class="px-3 py-2 text-right font-medium">
									<span class="inline-flex items-center gap-0.5"
										>{m.capacity_cpu_recent()}<HelpTip
											topic="metrics/sermon_cpu_utilization_ratio"
										/></span
									>
								</th>
								<th scope="col" class="px-3 py-2 text-right font-medium">{m.capacity_change()}</th>
								<th scope="col" class="w-32 px-3 py-2 font-medium">{m.capacity_course()}</th>
								<th scope="col" class="px-3 py-2 text-right font-medium">
									<span class="inline-flex items-center gap-0.5"
										>{m.capacity_memory_recent()}<HelpTip
											topic="metrics/sermon_memory_utilization_ratio"
										/></span
									>
								</th>
								<th scope="col" class="px-3 py-2 text-right font-medium">{m.capacity_change()}</th>
								<th scope="col" class="w-32 px-3 py-2 font-medium">{m.capacity_course()}</th>
							</tr>
						</thead>
						<tbody>
							{#each shownNodes as n (n.nodeId)}
								<tr class="border-t">
									<th scope="row" class="px-3 py-2 text-left font-normal">
										<a
											class="underline-offset-2 hover:underline"
											href={resolve('/(app)/nodes/[id]', { id: n.nodeId })}
										>
											{displayText(n.hostname)}
										</a>
									</th>
									{#each [n.cpu, n.memory] as trend, i (i)}
										<td class="px-3 py-2 text-right font-mono tabular-nums">
											{trend ? formatPercent(trend.recent, locale) : '–'}
										</td>
										<td class="px-3 py-2 text-right font-mono tabular-nums">
											{trend?.change === undefined ? '–' : change.format(trend.change)}
										</td>
										<td class="px-3 py-2">
											<Sparkline
												values={(i === 0
													? sparklines[n.nodeId]?.cpu
													: sparklines[n.nodeId]?.memory) ?? []}
												height={20}
											/>
										</td>
									{/each}
								</tr>
							{/each}
						</tbody>
					</table>
				</div>
				{#if !allNodes && ranked.length > FIRST}
					<Button variant="outline" size="sm" class="self-start" onclick={() => (allNodes = true)}>
						{m.capacity_show_all_nodes({ count: ranked.length })}
					</Button>
				{/if}
			{/if}
		</section>
	{/if}
</div>
