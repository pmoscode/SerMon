<script lang="ts">
	import X from '@lucide/svelte/icons/x';
	import { MAX_NODES, MIN_NODES } from '#lib/compare/compare.js';
	import { displayText } from '#lib/logs/text.js';
	import { m } from '#lib/paraglide/messages.js';

	/** The nodes of a comparison: the chosen ones to remove, the others of the fleet to add. */
	let {
		options,
		selected,
		onadd,
		onremove
	}: {
		options: { nodeId: string; hostname: string }[];
		selected: string[];
		onadd: (nodeId: string) => void;
		onremove: (nodeId: string) => void;
	} = $props();

	const names = $derived(new Map(options.map((o) => [o.nodeId, displayText(o.hostname)])));
	const offered = $derived(options.filter((o) => !selected.includes(o.nodeId)));
	const full = $derived(selected.length >= MAX_NODES);
	const nameOf = (id: string) => names.get(id) ?? m.compare_unknown_node();
	const uid = $props.id();
</script>

<div class="flex flex-col gap-2">
	{#if selected.length > 0}
		<ul class="flex flex-wrap gap-2" aria-label={m.compare_selected()}>
			{#each selected as id (id)}
				<li
					class="inline-flex items-center gap-1 rounded-md border bg-muted/40 py-0.5 pr-1 pl-2 text-sm"
				>
					<span class={names.has(id) ? '' : 'text-muted-foreground italic'}>{nameOf(id)}</span>
					<button
						type="button"
						class="rounded-sm p-0.5 text-muted-foreground hover:text-foreground focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
						aria-label={m.compare_remove({ host: nameOf(id) })}
						title={m.compare_remove({ host: nameOf(id) })}
						onclick={() => onremove(id)}
					>
						<X class="size-3.5" aria-hidden="true" />
					</button>
				</li>
			{/each}
		</ul>
	{/if}
	<div class="flex flex-wrap items-center gap-2 text-sm">
		<label for="{uid}-add">{m.compare_add()}</label>
		<select
			id="{uid}-add"
			class="rounded-md border bg-background px-2 py-1 text-sm disabled:opacity-50"
			disabled={full || offered.length === 0}
			onchange={(e) => {
				const id = e.currentTarget.value;
				e.currentTarget.value = '';
				if (id) onadd(id);
			}}
		>
			<option value="">{m.compare_add_placeholder()}</option>
			{#each offered as o (o.nodeId)}
				<option value={o.nodeId}>{displayText(o.hostname)}</option>
			{/each}
		</select>
	</div>
	{#if full}
		<p class="text-sm text-muted-foreground">{m.compare_limit({ max: MAX_NODES })}</p>
	{:else if selected.length < MIN_NODES}
		<p class="text-sm text-muted-foreground">{m.compare_pick_hint({ min: MIN_NODES })}</p>
	{/if}
</div>
