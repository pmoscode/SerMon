<script lang="ts">
	/**
	 * CPU busy of the last minutes as a line (values 0..1), as wide as its container; decoration,
	 * the parent names the value.
	 */
	let { values, height = 28 }: { values: number[]; height?: number } = $props();

	const points = $derived(
		values.length < 2
			? ''
			: values
					.map((v, i) => `${((i / (values.length - 1)) * 100).toFixed(2)},${(1 - v).toFixed(3)}`)
					.join(' ')
	);
</script>

{#if points}
	<svg
		class="block w-full text-chart-1"
		{height}
		viewBox="0 -0.05 100 1.1"
		preserveAspectRatio="none"
		aria-hidden="true"
	>
		<polyline
			{points}
			fill="none"
			stroke="currentColor"
			stroke-width="1.5"
			stroke-linejoin="round"
			vector-effect="non-scaling-stroke"
		/>
	</svg>
{/if}
