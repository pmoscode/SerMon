<script lang="ts">
	import { m } from '#lib/paraglide/messages.js';
	import { tagPairs } from '#lib/tags/tags.js';

	/**
	 * The tags of a node as small labels (M2-29). They come from admins of the organization and
	 * are shown as text only.
	 */
	let { tags, class: className = '' }: { tags: Record<string, string>; class?: string } = $props();

	const pairs = $derived(tagPairs(tags));
</script>

{#if pairs.length > 0}
	<ul class="flex flex-wrap gap-1 {className}" aria-label={m.tags_label()}>
		{#each pairs as pair (pair)}
			<li class="rounded border bg-muted/50 px-1.5 py-0.5 font-mono text-xs text-muted-foreground">
				{pair}
			</li>
		{/each}
	</ul>
{/if}
