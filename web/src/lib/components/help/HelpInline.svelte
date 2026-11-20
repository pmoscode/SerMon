<script lang="ts">
	import type { Inline } from '#lib/help/types.js';
	import { m } from '#lib/paraglide/messages.js';
	import { helpState } from '#lib/stores/help.svelte.js';
	import HelpInline from './HelpInline.svelte';

	/** Renders help text as text nodes only – help files never inject markup. */
	let { content }: { content: Inline[] } = $props();

	/** A path of this app ("/fleet"), never protocol-relative ("//host", "/\\host"). */
	const appPath = (href: string) => /^\/(?![/\\])/.test(href);
</script>

{#each content as inline, i (i)}
	{#if inline.type === 'text'}{inline.text}{:else if inline.type === 'strong'}<strong
			><HelpInline content={inline.content} /></strong
		>{:else if inline.type === 'em'}<em><HelpInline content={inline.content} /></em
		>{:else if inline.type === 'code'}<code class="rounded bg-muted px-1 font-mono text-[0.9em]"
			>{inline.text}</code
		>{:else if inline.href.startsWith('help:')}<button
			type="button"
			class="underline underline-offset-2 hover:text-foreground"
			onclick={() => helpState.open(inline.href.slice('help:'.length))}
			><HelpInline content={inline.content} /></button
		>{:else if inline.href.startsWith('https://')}<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- external documentation, not an app route --><a
			href={inline.href}
			target="_blank"
			rel="noopener noreferrer"
			class="underline underline-offset-2"
			><HelpInline content={inline.content} /><span class="sr-only"> ({m.help_new_tab()})</span></a
		>{:else if appPath(inline.href)}<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- app paths from our own help files, checked by scripts/help.js --><a
			href={inline.href}
			class="underline underline-offset-2"
			onclick={() => helpState.close()}><HelpInline content={inline.content} /></a
		>{:else}<HelpInline content={inline.content} />{/if}
{/each}
