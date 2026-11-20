<script lang="ts">
	import type { Status } from '#lib/fleet/status.js';
	import { m } from '#lib/paraglide/messages.js';
	import StatusBadge from './StatusBadge.svelte';

	/** Key figures of the fleet; each status is a filter button. */
	let {
		total,
		counts,
		active,
		onSelect
	}: {
		total: number;
		counts: Record<Status, number>;
		active: Status | '';
		onSelect: (s: Status | '') => void;
	} = $props();

	const order: Status[] = ['critical', 'warning', 'offline', 'ok'];
</script>

<section aria-label={m.fleet_summary()} class="flex flex-wrap gap-2">
	<button
		type="button"
		class="rounded-lg border px-4 py-2 text-left aria-pressed:border-primary aria-pressed:ring-2 aria-pressed:ring-primary/30"
		aria-pressed={active === ''}
		onclick={() => onSelect('')}
	>
		<span class="block text-2xl font-semibold tabular-nums">{total}</span>
		<span class="text-xs text-muted-foreground">{m.fleet_total()}</span>
	</button>
	{#each order as status (status)}
		<button
			type="button"
			class="rounded-lg border px-4 py-2 text-left aria-pressed:border-primary aria-pressed:ring-2 aria-pressed:ring-primary/30"
			aria-pressed={active === status}
			onclick={() => onSelect(active === status ? '' : status)}
		>
			<span class="block text-2xl font-semibold tabular-nums">{counts[status]}</span>
			<StatusBadge {status} />
		</button>
	{/each}
</section>
