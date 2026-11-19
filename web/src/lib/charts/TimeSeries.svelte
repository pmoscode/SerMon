<script lang="ts">
	import uPlot from 'uplot';
	import 'uplot/dist/uPlot.min.css';
	import { untrack, type Snippet } from 'svelte';
	import { formatValue, type Unit } from '#lib/format/format.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { m } from '#lib/paraglide/messages.js';
	import { timeRange } from '#lib/stores/timerange.svelte.js';
	import { isDark, themeState } from '#lib/theme/theme.svelte.js';
	import { align, summarize, type ChartSeries } from './data.js';

	interface Props {
		/** Visible title, also the accessible name of the chart. */
		title: string;
		unit: Unit;
		series: ChartSeries[];
		height?: number;
		/** Show byte rates as bit/s. */
		bits?: boolean;
		loading?: boolean;
		/** A message when loading failed. */
		error?: string;
		/** Next to the title, e.g. a HelpTip. */
		actions?: Snippet;
	}

	let {
		title,
		unit,
		series,
		height = 200,
		bits = false,
		loading = false,
		error = '',
		actions
	}: Props = $props();

	/** All charts with this key share the crosshair. */
	const SYNC_KEY = 'sermon-time';
	const id = $props.id();

	let container = $state<HTMLDivElement>();
	let width = $state(0);
	let chart = $state<uPlot>();

	const data = $derived(align(series));
	const summaries = $derived(summarize(series, timeRange.range));
	const dark = $derived(isDark(themeState.current, themeState.systemDark));
	const labels = $derived(series.map((s) => s.label).join('\u0000'));
	const empty = $derived(!loading && !error && summaries.length === 0);

	const format = (value: number) => formatValue(unit, value, localeState.current, { bits });
	const dateTime = (ms: number) =>
		new Intl.DateTimeFormat(localeState.current, { dateStyle: 'short', timeStyle: 'short' }).format(
			ms
		);

	function token(name: string): string {
		return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
	}

	function timeAxis(splits: number[]): string[] {
		const span = timeRange.range.to - timeRange.range.from;
		const options: Intl.DateTimeFormatOptions =
			span > 2 * 86400_000
				? { day: '2-digit', month: '2-digit' }
				: { hour: '2-digit', minute: '2-digit' };
		const formatter = new Intl.DateTimeFormat(localeState.current, options);
		return splits.map((s) => formatter.format(s * 1000));
	}

	function options(chartWidth: number): uPlot.Options {
		const text = token('--muted-foreground');
		const grid = token('--border');
		const axis = {
			stroke: text,
			grid: { stroke: grid, width: 1 },
			ticks: { stroke: grid, width: 1 }
		};
		const range = timeRange.range;
		return {
			width: chartWidth,
			height,
			scales: {
				x: { time: true, min: range.from / 1000, max: range.to / 1000 },
				y: { range: (_u, min, max) => [Math.min(0, min), max > min ? max : min + 1] }
			},
			axes: [
				{ ...axis, values: (_u, splits) => timeAxis(splits) },
				{ ...axis, size: 72, values: (_u, splits) => splits.map(format) }
			],
			series: [
				{ value: (_u, v) => (v == null ? '–' : dateTime(v * 1000)) },
				...series.map((s, i) => ({
					label: s.label,
					stroke: token(`--chart-${(i % 5) + 1}`),
					width: 1.5,
					value: (_u: uPlot, v: number | null) => (v == null ? '–' : format(v))
				}))
			],
			cursor: {
				sync: { key: SYNC_KEY },
				drag: { x: true, y: false, setScale: false },
				bind: {
					dblclick: () => () => {
						timeRange.reset();
						return null;
					}
				}
			},
			hooks: {
				setSelect: [
					(u) => {
						if (u.select.width < 2) return;
						const from = u.posToVal(u.select.left, 'x') * 1000;
						const to = u.posToVal(u.select.left + u.select.width, 'x') * 1000;
						u.setSelect({ left: 0, top: 0, width: 0, height: 0 }, false);
						timeRange.zoom(from, to);
					}
				]
			}
		};
	}

	// (Re)create the chart when its look changes: theme, language, unit or the set of series.
	$effect(() => {
		void [dark, localeState.current, unit, bits, labels];
		if (!container) return;
		const target = container;
		const u = untrack(() => new uPlot(options(width || target.clientWidth || 600), data, target));
		chart = u;
		return () => {
			u.destroy();
			chart = undefined;
		};
	});

	$effect(() => {
		chart?.setData(data, false);
	});

	$effect(() => {
		const { from, to } = timeRange.range;
		chart?.setScale('x', { min: from / 1000, max: to / 1000 });
	});

	$effect(() => {
		if (chart && width > 0) chart.setSize({ width, height });
	});
</script>

<figure class="flex flex-col gap-2" aria-labelledby="{id}-title">
	<figcaption class="flex items-center gap-1 text-sm font-medium">
		<span id="{id}-title">{title}</span>
		{@render actions?.()}
	</figcaption>
	<div class="relative" bind:clientWidth={width}>
		<div bind:this={container} aria-hidden="true" class="sermon-chart"></div>
		{#if loading}
			<p
				role="status"
				class="absolute inset-0 grid place-items-center bg-background/60 text-sm text-muted-foreground"
			>
				{m.loading()}
			</p>
		{:else if error}
			<p
				role="alert"
				class="absolute inset-0 grid place-items-center bg-background/60 text-sm text-destructive"
			>
				{error}
			</p>
		{:else if empty}
			<p class="absolute inset-0 grid place-items-center text-sm text-muted-foreground">
				{m.chart_empty()}
			</p>
		{/if}
	</div>
	<div class="sr-only">
		<p>
			{m.chart_range({ from: dateTime(timeRange.range.from), to: dateTime(timeRange.range.to) })}
		</p>
		<ul>
			{#each summaries as s (s.label)}
				<li>
					{m.chart_summary({
						label: s.label,
						min: format(s.min),
						max: format(s.max),
						last: format(s.last)
					})}
				</li>
			{/each}
		</ul>
	</div>
</figure>

<style>
	.sermon-chart :global(.u-legend) {
		font-size: 0.75rem;
		color: var(--muted-foreground);
	}
	.sermon-chart :global(.u-select) {
		background: color-mix(in oklch, var(--primary) 15%, transparent);
	}
</style>
