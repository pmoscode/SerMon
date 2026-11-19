<script lang="ts">
	import Info from '@lucide/svelte/icons/info';
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Popover from '#lib/components/ui/popover/index.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { m } from '#lib/paraglide/messages.js';
	import { helpState } from '#lib/stores/help.svelte.js';
	import HelpInline from './HelpInline.svelte';

	/** ⓘ next to a metric or key figure: the summary of a help topic, more in the help panel. */
	let { topic }: { topic: string } = $props();

	let open = $state(false);
	const doc = $derived(helpState.get(topic));

	$effect(() => {
		void helpState.load(localeState.current);
	});

	function more() {
		open = false;
		helpState.open(topic);
	}
</script>

{#if doc}
	<Popover.Root bind:open>
		<Popover.Trigger
			class="inline-flex size-6 items-center justify-center rounded-full text-muted-foreground hover:text-foreground focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
			aria-label={m.help_about({ title: doc.title })}
		>
			<Info class="size-4" aria-hidden="true" />
		</Popover.Trigger>
		<Popover.Content class="w-80 gap-2">
			<p class="font-medium">{doc.title}</p>
			<p class="text-muted-foreground"><HelpInline content={doc.summary} /></p>
			{#if doc.body.length}
				<Button variant="link" size="sm" class="self-start px-0" onclick={more}
					>{m.help_more()}</Button
				>
			{/if}
		</Popover.Content>
	</Popover.Root>
{/if}
