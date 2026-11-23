<script lang="ts">
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import { formatNumber } from '#lib/format/format.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import type { HistogramData } from '#lib/logs/explorer.svelte.js';
	import { buckets, groups, type Group } from '#lib/logs/histogram.js';
	import { groupLabel } from '#lib/logs/labels.js';
	import { m } from '#lib/paraglide/messages.js';

	/**
	 * Lines per severity over the range as stacked bars. Dragging across it zooms to that part of
	 * the range; the time range buttons do the same by keyboard.
	 */
	let {
		data,
		loading = false,
		failed = false,
		onZoom
	}: {
		data: HistogramData | undefined;
		loading?: boolean;
		failed?: boolean;
		onZoom: (from: number, to: number) => void;
	} = $props();

	const WIDTH = 1000;
	const HEIGHT = 100;
	/** A drag below this share of the width is a click. */
	const MIN_DRAG = 0.01;

	const fill: Record<Group, string> = {
		error: 'fill-red-600 dark:fill-red-400',
		warning: 'fill-amber-500 dark:fill-amber-400',
		info: 'fill-sky-600 dark:fill-sky-400',
		debug: 'fill-muted-foreground/40'
	};
	const swatch: Record<Group, string> = {
		error: 'bg-red-600 dark:bg-red-400',
		warning: 'bg-amber-500 dark:bg-amber-400',
		info: 'bg-sky-600 dark:bg-sky-400',
		debug: 'bg-muted-foreground/40'
	};

	const bars = $derived(data ? buckets(data.series, data.step, data.range) : []);
	const top = $derived(Math.max(1, ...bars.map((b) => b.total)));
	const totals = $derived(
		Object.fromEntries(
			groups.map((g) => [g, bars.reduce((sum, b) => sum + b.counts[g], 0)])
		) as Record<Group, number>
	);
	const sum = $derived(groups.reduce((s, g) => s + totals[g], 0));
	const span = $derived(data ? data.range.to - data.range.from : 1);
	const dateTime = (ms: number) =>
		new Intl.DateTimeFormat(localeState.current, { dateStyle: 'short', timeStyle: 'short' }).format(
			ms
		);

	let area = $state<HTMLDivElement>();
	let drag = $state<{ start: number; end: number } | undefined>(undefined);

	function share(event: PointerEvent): number {
		const rect = area!.getBoundingClientRect();
		return Math.min(1, Math.max(0, (event.clientX - rect.left) / rect.width));
	}

	function down(event: PointerEvent) {
		if (!data || event.button !== 0) return;
		area!.setPointerCapture?.(event.pointerId);
		const at = share(event);
		drag = { start: at, end: at };
	}

	function move(event: PointerEvent) {
		if (drag) drag = { ...drag, end: share(event) };
	}

	function up() {
		if (!drag || !data) return;
		const [a, b] = [Math.min(drag.start, drag.end), Math.max(drag.start, drag.end)];
		drag = undefined;
		if (b - a < MIN_DRAG) return;
		onZoom(Math.round(data.range.from + a * span), Math.round(data.range.from + b * span));
	}
</script>

<figure class="flex flex-col gap-2" aria-labelledby="log-histogram-title">
	<figcaption class="flex flex-wrap items-center gap-x-4 gap-y-1 text-sm">
		<span id="log-histogram-title" class="inline-flex items-center gap-1 font-medium">
			{m.logs_histogram_title()}
			<HelpTip topic="pages/logs" />
		</span>
		{#each groups as g (g)}
			<span class="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
				<span class="size-2.5 rounded-sm {swatch[g]}" aria-hidden="true"></span>
				{groupLabel(g)}
				<span class="font-medium text-foreground tabular-nums"
					>{formatNumber(totals[g], localeState.current)}</span
				>
			</span>
		{/each}
	</figcaption>
	<div
		bind:this={area}
		class="relative h-24 cursor-crosshair touch-none rounded-md border border-border select-none"
		onpointerdown={down}
		onpointermove={move}
		onpointerup={up}
		onpointercancel={() => (drag = undefined)}
		aria-hidden="true"
	>
		<svg
			viewBox="0 0 {WIDTH} {HEIGHT}"
			preserveAspectRatio="none"
			class="size-full"
			data-testid="log-histogram"
		>
			{#each bars as bar (bar.start)}
				{@const x = ((bar.start - data!.range.from) / span) * WIDTH}
				{@const w = Math.max(((bar.end - bar.start) / span) * WIDTH - 0.5, 0.5)}
				{#each groups as g, gi (g)}
					{#if bar.counts[g] > 0}
						{@const below = groups.slice(0, gi).reduce((s, o) => s + bar.counts[o], 0)}
						{@const h = (bar.counts[g] / top) * HEIGHT}
						<rect
							{x}
							width={w}
							y={HEIGHT - ((below + bar.counts[g]) / top) * HEIGHT}
							height={h}
							class={fill[g]}
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
				{m.logs_histogram_failed()}
			</p>
		{:else if data && sum === 0}
			<p class="absolute inset-0 grid place-items-center text-sm text-muted-foreground">
				{m.logs_histogram_empty()}
			</p>
		{/if}
	</div>
	{#if data}
		<div class="flex justify-between text-xs text-muted-foreground">
			<span>{dateTime(data.range.from)}</span>
			<span>{dateTime(data.range.to)}</span>
		</div>
	{/if}
	<div class="sr-only" role="status">
		{#if loading}
			<p>{m.loading()}</p>
		{:else if failed}
			<p>{m.logs_histogram_failed()}</p>
		{:else if data}
			<p>
				{m.logs_histogram_summary({
					count: formatNumber(sum, localeState.current),
					from: dateTime(data.range.from),
					to: dateTime(data.range.to)
				})}
			</p>
		{/if}
	</div>
</figure>
