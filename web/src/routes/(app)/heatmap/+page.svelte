<script lang="ts">
	import { durationMs, timestampFromMs, timestampMs } from '@bufbuild/protobuf/wkt';
	import { resolve } from '$app/paths';
	import { heatmapApi } from '#lib/api/heatmap.js';
	import { HeatmapKind, type GetHeatmapResponse } from '#lib/api/gen/sermon/web/v1/heatmap_pb.js';
	import FleetNav from '#lib/components/fleet/FleetNav.svelte';
	import HeatmapGrid from '#lib/components/heatmap/HeatmapGrid.svelte';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import { formatPercent } from '#lib/format/format.js';
	import {
		heatmapRange,
		orderRows,
		SCALE,
		summarize,
		type RowOrder
	} from '#lib/heatmap/heatmap.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { displayText } from '#lib/logs/text.js';
	import { m } from '#lib/paraglide/messages.js';

	/** The fleet heatmap (KONZEPT §16, 7; M4-27): nodes × time, coloured by one kind. */
	const REFRESH = 60_000;

	const kinds = [
		{
			kind: HeatmapKind.CPU,
			label: () => m.heatmap_cpu(),
			help: 'metrics/sermon_cpu_utilization_ratio'
		},
		{
			kind: HeatmapKind.MEMORY,
			label: () => m.heatmap_memory(),
			help: 'metrics/sermon_memory_utilization_ratio'
		},
		{ kind: HeatmapKind.IO, label: () => m.heatmap_io(), help: 'metrics/sermon_disk_busy_ratio' },
		{
			kind: HeatmapKind.ANOMALY,
			label: () => m.heatmap_anomaly(),
			help: 'metrics/sermon_anomaly_rate_ratio'
		}
	];
	// Its own range, relative to now: zoom isn't needed here, and sharing the global time range
	// would split a chunk of the node page off (M2-34); its own messages for the same reason.
	const ranges = [
		{ span: 3600_000, label: () => m.heatmap_range_1h() },
		{ span: 6 * 3600_000, label: () => m.heatmap_range_6h() },
		{ span: 86_400_000, label: () => m.heatmap_range_24h() },
		{ span: 7 * 86_400_000, label: () => m.heatmap_range_7d() },
		{ span: 30 * 86_400_000, label: () => m.heatmap_range_30d() }
	];
	let span = $state(86_400_000);
	let now = $state(Date.now());
	let kind = $state(HeatmapKind.CPU);
	let order = $state<RowOrder>('name');
	let asTable = $state(false);
	let heatmap = $state<GetHeatmapResponse | undefined>();
	let status = $state<'loading' | 'ready' | 'error'>('loading');
	let request = 0;

	async function load() {
		const current = ++request;
		const { from, to } = heatmapRange({ from: now - span, to: now });
		try {
			const res = await heatmapApi.getHeatmap({
				kind,
				from: timestampFromMs(from),
				to: timestampFromMs(to)
			});
			if (current !== request) return;
			heatmap = res;
			status = 'ready';
		} catch {
			if (current === request) status = 'error';
		}
	}

	// load() reads kind, span and now before its first await: the effect follows them.
	$effect(() => {
		void load();
	});
	$effect(() => {
		const timer = setInterval(() => (now = Date.now()), REFRESH);
		return () => clearInterval(timer);
	});

	const locale = $derived(localeState.current);
	const current = $derived(kinds.find((k) => k.kind === kind) ?? kinds[0]);
	const rows = $derived(orderRows(heatmap?.rows ?? [], order));
	const start = $derived(heatmap?.start ? timestampMs(heatmap.start) : 0);
	const step = $derived(heatmap?.step ? durationMs(heatmap.step) : 60_000);
	const time = $derived(
		new Intl.DateTimeFormat(locale, { dateStyle: 'short', timeStyle: 'short' })
	);
	const orders: { order: RowOrder; label: () => string }[] = [
		{ order: 'name', label: () => m.heatmap_order_name() },
		{ order: 'max', label: () => m.heatmap_order_max() },
		{ order: 'mean', label: () => m.heatmap_order_mean() }
	];
</script>

<svelte:head>
	<title>{m.heatmap_title()} · SerMon</title>
</svelte:head>

<FleetNav />

<div class="flex flex-col gap-4 pt-4">
	<div class="flex flex-wrap items-baseline gap-2">
		<h1 class="text-2xl font-semibold tracking-tight">{m.heatmap_title()}</h1>
		<HelpTip topic="pages/heatmap" />
	</div>

	<div class="flex flex-wrap items-center gap-3">
		<div class="flex gap-1" role="group" aria-label={m.heatmap_kind()}>
			{#each kinds as k (k.kind)}
				<Button
					variant="outline"
					size="sm"
					aria-pressed={kind === k.kind}
					class="aria-pressed:border-primary aria-pressed:ring-2 aria-pressed:ring-primary/30"
					onclick={() => (kind = k.kind)}
				>
					{k.label()}
				</Button>
			{/each}
		</div>
		<HelpTip topic={current.help} />
		<label class="flex items-center gap-2 text-sm">
			{m.heatmap_range()}
			<select
				class="rounded-md border bg-background px-2 py-1 text-sm"
				onchange={(e) => {
					span = Number(e.currentTarget.value);
					now = Date.now();
				}}
			>
				{#each ranges as r (r.span)}
					<option value={r.span} selected={r.span === span}>{r.label()}</option>
				{/each}
			</select>
		</label>
		<label class="flex items-center gap-2 text-sm">
			{m.heatmap_order()}
			<select class="rounded-md border bg-background px-2 py-1 text-sm" bind:value={order}>
				{#each orders as o (o.order)}
					<option value={o.order}>{o.label()}</option>
				{/each}
			</select>
		</label>
		<Button variant="outline" size="sm" aria-pressed={asTable} onclick={() => (asTable = !asTable)}>
			{m.heatmap_as_table()}
		</Button>
	</div>

	<!-- The legend: the colours and what they mean; empty cells have no data. -->
	<div
		class="flex flex-wrap items-center gap-2 text-xs text-muted-foreground"
		aria-label={m.heatmap_legend()}
		role="group"
	>
		<span>{formatPercent(0, locale)}</span>
		<span class="flex" aria-hidden="true">
			{#each SCALE as color (color)}
				<span class="h-3 w-5" style:background-color={color}></span>
			{/each}
		</span>
		<span>{formatPercent(1, locale)}</span>
		<span class="ml-3 inline-flex items-center gap-1">
			<span class="h-3 w-5 border border-dashed" aria-hidden="true"></span>{m.heatmap_no_data()}
		</span>
		<span class="sr-only">{m.heatmap_legend_text()}</span>
	</div>

	{#if status === 'loading'}
		<p role="status" class="text-muted-foreground">{m.loading()}</p>
	{:else if status === 'error'}
		<div class="flex flex-col items-start gap-3">
			<p role="alert" class="text-destructive">{m.error_unavailable()}</p>
			<Button variant="outline" onclick={() => void load()}>{m.retry()}</Button>
		</div>
	{:else if rows.length === 0}
		<p class="text-sm text-muted-foreground">{m.heatmap_empty()}</p>
	{:else}
		{#if heatmap?.truncated}
			<p class="text-sm text-muted-foreground">{m.heatmap_truncated({ count: rows.length })}</p>
		{/if}
		{#if asTable}
			<div class="overflow-x-auto rounded-lg border">
				<table class="w-full text-sm">
					<caption class="sr-only">{m.heatmap_table({ label: current.label() })}</caption>
					<thead class="bg-muted/50 text-left text-xs text-muted-foreground">
						<tr>
							<th scope="col" class="px-3 py-2 font-medium">{m.unusual_node()}</th>
							<th scope="col" class="px-3 py-2 text-right font-medium">{m.heatmap_mean()}</th>
							<th scope="col" class="px-3 py-2 text-right font-medium">{m.heatmap_max()}</th>
							<th scope="col" class="px-3 py-2 font-medium">{m.heatmap_max_at()}</th>
						</tr>
					</thead>
					<tbody>
						{#each rows as r (r.nodeId)}
							{@const s = summarize(r.values)}
							<tr class="border-t">
								<th scope="row" class="px-3 py-2 text-left font-normal">
									<a
										class="underline-offset-2 hover:underline"
										href={resolve('/(app)/nodes/[id]', { id: r.nodeId })}
									>
										{displayText(r.hostname)}
									</a>
								</th>
								<td class="px-3 py-2 text-right font-mono tabular-nums"
									>{s ? formatPercent(s.mean, locale) : '–'}</td
								>
								<td class="px-3 py-2 text-right font-mono tabular-nums"
									>{s ? formatPercent(s.max, locale) : '–'}</td
								>
								<td class="px-3 py-2 tabular-nums"
									>{s ? time.format(start + s.maxAt * step) : '–'}</td
								>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		{:else}
			<HeatmapGrid {rows} {start} {step} columns={heatmap?.columns ?? 0} label={current.label()} />
		{/if}
	{/if}
</div>
