<script lang="ts">
	import { diffLines } from '#lib/rules/diff.js';
	import { m } from '#lib/paraglide/messages.js';

	/** A line diff of two rule documents; added and removed lines also say so in words. */
	let { before, after, label }: { before: string; after: string; label: string } = $props();
	const lines = $derived(diffLines(before, after));
</script>

<figure class="overflow-x-auto rounded-md border text-xs">
	<figcaption class="sr-only">{label}</figcaption>
	<pre class="font-mono"><code
			>{#each lines as l, i (i)}<span
					class={l.kind === 'add'
						? 'block bg-emerald-500/15 text-emerald-900 dark:text-emerald-200'
						: l.kind === 'del'
							? 'block bg-red-500/15 text-red-900 line-through decoration-red-700/40 dark:text-red-200'
							: 'block text-muted-foreground'}
					><span aria-hidden="true">{l.kind === 'add' ? '+ ' : l.kind === 'del' ? '- ' : '  '}</span
					><span class="sr-only"
						>{l.kind === 'add'
							? `${m.diff_added()} `
							: l.kind === 'del'
								? `${m.diff_removed()} `
								: ''}</span
					>{l.text}</span
				>{/each}</code
		></pre>
</figure>
