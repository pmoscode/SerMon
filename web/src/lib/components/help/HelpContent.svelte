<script lang="ts">
	import type { Block } from '#lib/help/types.js';
	import HelpInline from './HelpInline.svelte';

	let { blocks }: { blocks: Block[] } = $props();
</script>

<div class="flex flex-col gap-3 text-sm leading-relaxed">
	{#each blocks as block, i (i)}
		{#if block.type === 'heading' && block.level === 2}
			<h3 class="mt-2 font-semibold"><HelpInline content={block.content} /></h3>
		{:else if block.type === 'heading'}
			<h4 class="font-medium"><HelpInline content={block.content} /></h4>
		{:else if block.type === 'paragraph'}
			<p><HelpInline content={block.content} /></p>
		{:else if block.type === 'list' && block.ordered}
			<ol class="list-decimal space-y-1 pl-5">
				{#each block.items as item, j (j)}<li><HelpInline content={item} /></li>{/each}
			</ol>
		{:else if block.type === 'list'}
			<ul class="list-disc space-y-1 pl-5">
				{#each block.items as item, j (j)}<li><HelpInline content={item} /></li>{/each}
			</ul>
		{:else if block.type === 'code'}
			<pre class="overflow-x-auto rounded-md bg-muted p-3 font-mono text-xs"><code
					>{block.text}</code
				></pre>
		{/if}
	{/each}
</div>
