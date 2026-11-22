<script lang="ts">
	import Pencil from '@lucide/svelte/icons/pencil';
	import type { Node } from '#lib/api/gen/sermon/web/v1/node_pb.js';
	import TagList from '#lib/components/fleet/TagList.svelte';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import { m } from '#lib/paraglide/messages.js';
	import { sessionState } from '#lib/stores/session.svelte.js';

	/**
	 * The node's tags (M2-29): shown to everyone who sees the node, set by admins of its group
	 * with a step-up (the form loads on demand). They steer rules, policies and silences, never
	 * rights.
	 */
	let { node, onchange }: { node: Node; onchange: () => void } = $props();

	const allowed = $derived.by(() => {
		const groups = sessionState.current?.tagGroupIds ?? [];
		return groups.includes('*') || groups.includes(node.groupId);
	});
	const empty = $derived(Object.keys(node.tags).length === 0);

	let editing = $state(false);

	function done(saved: boolean) {
		editing = false;
		if (saved) onchange();
	}
</script>

<section class="flex flex-col gap-2" aria-labelledby="tags-title">
	<div class="flex flex-wrap items-center gap-2">
		<h2 id="tags-title" class="flex items-center gap-1 text-sm font-medium">
			{m.tags_label()}
			<HelpTip topic="concepts/tags" />
		</h2>
		{#if !editing}
			<TagList tags={node.tags} />
			{#if empty}
				<span class="text-sm text-muted-foreground">{m.tags_none()}</span>
			{/if}
			{#if allowed}
				<Button variant="ghost" size="sm" onclick={() => (editing = true)}>
					<Pencil aria-hidden="true" />
					{m.tags_edit()}
				</Button>
			{/if}
		{/if}
	</div>
	{#if editing}
		{#await import('./NodeTagsForm.svelte')}
			<p role="status" class="text-sm text-muted-foreground">{m.loading()}</p>
		{:then { default: NodeTagsForm }}
			<NodeTagsForm {node} ondone={done} />
		{/await}
	{/if}
</section>
