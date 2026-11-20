<script lang="ts">
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import { CRITICAL, WARNING } from '#lib/fleet/status.js';
	import { formatPercent } from '#lib/format/format.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { m } from '#lib/paraglide/messages.js';

	/** A small gauge for a share 0..1 (CPU, memory, disk) with value as text. */
	let { label, value, topic }: { label: string; value: number | undefined; topic?: string } =
		$props();

	const percent = $derived(
		value === undefined ? undefined : Math.round(Math.min(1, Math.max(0, value)) * 100)
	);
	// SVG instead of a width style: the CSP allows no inline styles.
	const tone = $derived(
		value === undefined
			? ''
			: value >= CRITICAL
				? 'fill-red-600 dark:fill-red-500'
				: value >= WARNING
					? 'fill-amber-500'
					: 'fill-primary'
	);
</script>

<div class="flex flex-col gap-1">
	<div class="flex items-center justify-between gap-2 text-xs">
		<span class="flex items-center gap-0.5 text-muted-foreground">
			{label}
			{#if topic}<HelpTip {topic} />{/if}
		</span>
		<span class="font-mono tabular-nums">
			{value === undefined ? m.value_missing() : formatPercent(value, localeState.current)}
		</span>
	</div>
	{#if percent !== undefined}
		<div
			class="h-1.5 overflow-hidden rounded-full bg-muted"
			role="meter"
			aria-label={label}
			aria-valuemin={0}
			aria-valuemax={100}
			aria-valuenow={percent}
			aria-valuetext={formatPercent(value ?? 0, localeState.current)}
		>
			<svg
				class="block h-full w-full"
				viewBox="0 0 100 1"
				preserveAspectRatio="none"
				aria-hidden="true"
			>
				<rect width={percent} height="1" class={tone} />
			</svg>
		</div>
	{/if}
</div>
