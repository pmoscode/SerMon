<script lang="ts">
	import ZoomOut from '@lucide/svelte/icons/zoom-out';
	import { Button } from '#lib/components/ui/button/index.js';
	import { m } from '#lib/paraglide/messages.js';
	import { presets, timeRange, type Preset } from '#lib/stores/timerange.svelte.js';

	const labels: Record<Preset, () => string> = {
		'15m': () => m.range_15m(),
		'1h': () => m.range_1h(),
		'6h': () => m.range_6h(),
		'24h': () => m.range_24h(),
		'7d': () => m.range_7d(),
		'30d': () => m.range_30d()
	};
</script>

<div class="flex flex-wrap items-center gap-2">
	<div role="group" aria-label={m.range_label()} class="inline-flex gap-1">
		{#each presets as preset (preset)}
			{@const active = timeRange.live && timeRange.preset === preset}
			<Button
				variant={active ? 'secondary' : 'ghost'}
				size="sm"
				aria-pressed={active}
				onclick={() => timeRange.setPreset(preset)}
			>
				{labels[preset]()}
			</Button>
		{/each}
	</div>
	{#if !timeRange.live}
		<Button variant="outline" size="sm" onclick={() => timeRange.reset()}>
			<ZoomOut aria-hidden="true" />
			{m.range_reset_zoom()}
		</Button>
	{/if}
</div>
