<script lang="ts">
	import type { NodeEventSeries } from '#lib/api/gen/sermon/web/v1/security_event_pb.js';
	import { timestampMs } from '@bufbuild/protobuf/wkt';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import { formatNumber } from '#lib/format/format.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { m } from '#lib/paraglide/messages.js';
	import { stack } from '#lib/security/chart.js';

	/**
	 * Failed logins over the range as bars stacked by node (KONZEPT §16.4). Dragging across it
	 * zooms to that part of the range. The legend names every colour with its count, so the chart
	 * doesn't depend on colour alone.
	 */
	let {
		series,
		step,
		range,
		names,
		loading = false,
		failed = false,
		onZoom
	}: {
		series: NodeEventSeries[];
		/** The bucket width in ms. */
		step: number;
		range: { from: number; to: number };
		/** Host names by node ID. */
		names: Map<string, string>;
		loading?: boolean;
		failed?: boolean;
		onZoom: (from: number, to: number) => void;
	} = $props();

	const WIDTH = 1000;
	const HEIGHT = 100;
	const MIN_DRAG = 0.01;
	const fills = [
		'fill-sky-600 dark:fill-sky-400',
		'fill-amber-500 dark:fill-amber-400',
		'fill-violet-600 dark:fill-violet-400',
		'fill-emerald-600 dark:fill-emerald-400',
		'fill-rose-600 dark:fill-rose-400',
		'fill-cyan-700 dark:fill-cyan-400'
	];
	const swatches = [
		'bg-sky-600 dark:bg-sky-400',
		'bg-amber-500 dark:bg-amber-400',
		'bg-violet-600 dark:bg-violet-400',
		'bg-emerald-600 dark:bg-emerald-400',
		'bg-rose-600 dark:bg-rose-400',
		'bg-cyan-700 dark:bg-cyan-400'
	];
	const restFill = 'fill-muted-foreground/40';
	const restSwatch = 'bg-muted-foreground/40';

	const data = $derived(
		stack(
			series.map((s) => ({
				node: s.nodeId,
				points: s.points.map((p) => ({ at: p.at ? timestampMs(p.at) : 0, count: Number(p.count) }))
			})),
			step,
			range
		)
	);
	const top = $derived(Math.max(1, ...data.bars.map((b) => b.total)));
	const sum = $derived(data.legend.reduce((s, e) => s + e.total, 0));
	const span = $derived(Math.max(1, range.to - range.from));
	const locale = $derived(localeState.current);
	const dateTime = (ms: number) =>
		new Intl.DateTimeFormat(locale, { dateStyle: 'short', timeStyle: 'short' }).format(ms);
	const entryName = (node: string | undefined) =>
		node === undefined ? m.secdash_chart_other() : (names.get(node) ?? node.slice(0, 8));

	let area = $state<HTMLDivElement>();
	let drag = $state<{ start: number; end: number } | undefined>(undefined);

	function share(event: PointerEvent): number {
		const rect = area!.getBoundingClientRect();
		return Math.min(1, Math.max(0, (event.clientX - rect.left) / rect.width));
	}

	function down(event: PointerEvent) {
		if (event.button !== 0 || loading) return;
		area!.setPointerCapture?.(event.pointerId);
		const at = share(event);
		drag = { start: at, end: at };
	}

	function up() {
		if (!drag) return;
		const [a, b] = [Math.min(drag.start, drag.end), Math.max(drag.start, drag.end)];
		drag = undefined;
		if (b - a < MIN_DRAG) return;
		onZoom(Math.round(range.from + a * span), Math.round(range.from + b * span));
	}
</script>

<figure class="flex flex-col gap-2" aria-labelledby="sec-chart-title">
	<figcaption class="flex flex-wrap items-center gap-x-4 gap-y-1 text-sm">
		<span id="sec-chart-title" class="inline-flex items-center gap-1 font-medium">
			{m.secdash_chart_title()}
			<HelpTip topic="figures/failed_logins" />
		</span>
		{#each data.legend as entry, i (entry.node ?? 'other')}
			<span class="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
				<span
					class="size-2.5 rounded-sm {entry.node === undefined ? restSwatch : swatches[i]}"
					aria-hidden="true"
				></span>
				{entryName(entry.node)}
				<span class="font-medium text-foreground tabular-nums"
					>{formatNumber(entry.total, locale)}</span
				>
			</span>
		{/each}
	</figcaption>
	<div
		bind:this={area}
		class="relative h-28 cursor-crosshair touch-none rounded-md border border-border select-none"
		onpointerdown={down}
		onpointermove={(e) => {
			if (drag) drag = { ...drag, end: share(e) };
		}}
		onpointerup={up}
		onpointercancel={() => (drag = undefined)}
		aria-hidden="true"
	>
		<svg
			viewBox="0 0 {WIDTH} {HEIGHT}"
			preserveAspectRatio="none"
			class="size-full"
			data-testid="failed-logins-chart"
		>
			{#each data.bars as bar (bar.start)}
				{@const x = ((bar.start - range.from) / span) * WIDTH}
				{@const w = Math.max(((bar.end - bar.start) / span) * WIDTH - 0.5, 0.5)}
				{#each data.legend as entry, i (entry.node ?? 'other')}
					{#if bar.counts[i] > 0}
						{@const below = bar.counts.slice(0, i).reduce((s, c) => s + c, 0)}
						<rect
							{x}
							width={w}
							y={HEIGHT - ((below + bar.counts[i]) / top) * HEIGHT}
							height={(bar.counts[i] / top) * HEIGHT}
							class={entry.node === undefined ? restFill : fills[i]}
						/>
					{/if}
				{/each}
			{/each}
		</svg>
		{#if drag}
			<div
				class="pointer-events-none absolute inset-y-0 bg-primary/15"
				style:left="{Math.min(drag.start, drag.end) * 100}%"
				style:width="{Math.abs(drag.end - drag.start) * 100}%"
			></div>
		{/if}
		{#if loading}
			<p class="absolute inset-0 grid place-items-center text-sm text-muted-foreground">
				{m.loading()}
			</p>
		{:else if failed}
			<p class="absolute inset-0 grid place-items-center text-sm text-muted-foreground">
				{m.secdash_chart_failed()}
			</p>
		{:else if sum === 0}
			<p class="absolute inset-0 grid place-items-center text-sm text-muted-foreground">
				{m.secdash_chart_empty()}
			</p>
		{/if}
	</div>
	<div class="flex justify-between text-xs text-muted-foreground">
		<span>{dateTime(range.from)}</span>
		<span>{dateTime(range.to)}</span>
	</div>
	<p class="sr-only" role="status">
		{#if loading}
			{m.loading()}
		{:else if failed}
			{m.secdash_chart_failed()}
		{:else}
			{m.secdash_chart_summary({
				count: formatNumber(sum, locale),
				nodes: formatNumber(series.length, locale),
				from: dateTime(range.from),
				to: dateTime(range.to)
			})}
		{/if}
	</p>
</figure>
