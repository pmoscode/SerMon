<script lang="ts">
	import { displayParts } from '#lib/logs/text.js';

	/**
	 * A log line from a host, as text only (never {@html}): without ANSI sequences, with control and
	 * bidi characters as visible, marked replacements.
	 */
	let { text }: { text: string } = $props();

	const parts = $derived(displayParts(text));
</script>

<span class="[overflow-wrap:anywhere] break-words whitespace-pre-wrap"
	>{#each parts as part, i (i)}{#if part.marker}<span
				class="rounded-sm bg-muted px-0.5 text-amber-700 dark:text-amber-400">{part.text}</span
			>{:else}{part.text}{/if}{/each}</span
>
