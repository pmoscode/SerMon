<script lang="ts">
	import { latest, seriesName, type WidgetSeries } from '#lib/dashboards/source.js';
	import { formatValue, type Unit } from '#lib/format/format.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { displayText } from '#lib/logs/text.js';
	import { m } from '#lib/paraglide/messages.js';

	/**
	 * The latest value of one node against its range: shares on 0 to 100 %, other units against
	 * the highest value of the range. Several series (labels): the highest one, named.
	 */
	let {
		unit,
		series,
		labels = {}
	}: { unit: Unit; series: WidgetSeries[]; labels?: Record<string, string> } = $props();

	const candidates = $derived(
		series.flatMap((s) => {
			const p = latest(s);
			return p ? [{ s, value: p.value }] : [];
		})
	);
	const top = $derived(
		candidates.reduce<(typeof candidates)[number] | undefined>(
			(a, c) => (!a || c.value > a.value ? c : a),
			undefined
		)
	);
	const scale = $derived.by(() => {
		if (unit === 'ratio_ppm') return 1e6;
		if (unit === 'ratio' || unit === 'boolean') return 1;
		const peak = Math.max(
			0,
			...series.flatMap((s) => s.points.map((p) => p.max)).filter(Number.isFinite)
		);
		return peak > 0 ? peak : 1;
	});
	const share = $derived(top ? Math.min(1, Math.max(0, top.value / scale)) : 0);
	const text = $derived(top ? formatValue(unit, top.value, localeState.current) : '');
</script>

{#if !top}
	<p class="text-sm text-muted-foreground">{m.widget_no_data()}</p>
{:else}
	<div class="flex h-full flex-col items-center justify-center gap-1">
		<svg viewBox="0 0 100 56" class="w-full max-w-48" role="img" aria-label={text}>
			<path
				d="M 8 50 A 42 42 0 0 1 92 50"
				fill="none"
				stroke="currentColor"
				stroke-width="10"
				class="text-muted"
			/>
			<path
				d="M 8 50 A 42 42 0 0 1 92 50"
				fill="none"
				stroke="currentColor"
				stroke-width="10"
				pathLength="100"
				stroke-dasharray="{(share * 100).toFixed(1)} 100"
				class="text-primary"
			/>
		</svg>
		<p class="text-2xl font-semibold tabular-nums" aria-hidden="true">{text}</p>
		{#if candidates.length > 1}
			<p class="truncate text-xs text-muted-foreground">{displayText(seriesName(top.s, labels))}</p>
		{/if}
	</div>
{/if}
