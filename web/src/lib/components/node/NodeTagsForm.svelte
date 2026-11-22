<script lang="ts">
	import { api } from '#lib/api/client.js';
	import type { Node } from '#lib/api/gen/sermon/web/v1/node_pb.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { notifyError } from '#lib/notify/notify.js';
	import { m } from '#lib/paraglide/messages.js';
	import { withStepUp } from '#lib/stores/stepup.svelte.js';
	import { parseTags, tagsText } from '#lib/tags/tags.js';

	/**
	 * Editing the node's tags (M2-29), loaded on demand so the node page stays within its budget.
	 * Saving needs a step-up; the server checks the rights again.
	 */
	let { node, ondone }: { node: Node; ondone: (saved: boolean) => void } = $props();

	// The form starts from the tags at opening; a reload afterwards replaces the component.
	// svelte-ignore state_referenced_locally
	let text = $state(tagsText(node.tags));
	let busy = $state(false);
	let failure = $state('');

	async function save(event: SubmitEvent) {
		event.preventDefault();
		const parsed = parseTags(text);
		if ('error' in parsed) {
			failure = m.tags_invalid({ pair: parsed.error });
			return;
		}
		busy = true;
		failure = '';
		try {
			await withStepUp(() => api.node.setNodeTags({ nodeId: node.id, tags: parsed.tags }));
			ondone(true);
		} catch (error) {
			failure = notifyError(error);
		} finally {
			busy = false;
		}
	}
</script>

<form class="flex flex-wrap items-end gap-2" onsubmit={save}>
	<div class="flex min-w-64 flex-1 flex-col gap-1">
		<Label for="node-tags">{m.tags_input()}</Label>
		<Input
			id="node-tags"
			class="font-mono"
			bind:value={text}
			placeholder="env=prod, role=db"
			aria-describedby="node-tags-hint"
		/>
		<p id="node-tags-hint" class="text-xs text-muted-foreground">{m.tags_hint()}</p>
	</div>
	<Button type="submit" size="sm" disabled={busy}>{m.tags_save()}</Button>
	<Button type="button" variant="outline" size="sm" onclick={() => ondone(false)}>
		{m.tags_cancel()}
	</Button>
</form>
{#if failure}
	<p role="alert" class="text-sm text-destructive">{failure}</p>
{/if}
