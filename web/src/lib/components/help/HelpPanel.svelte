<script lang="ts">
	import * as Sheet from '#lib/components/ui/sheet/index.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { m } from '#lib/paraglide/messages.js';
	import { helpState } from '#lib/stores/help.svelte.js';
	import HelpContent from './HelpContent.svelte';
	import HelpInline from './HelpInline.svelte';

	/** The context help on the right (KONZEPT §17), opened via helpState.open(topic). */
	const doc = $derived(helpState.topic ? helpState.get(helpState.topic) : undefined);

	$effect(() => {
		if (helpState.topic) void helpState.load(localeState.current);
	});
</script>

<Sheet.Root
	open={helpState.topic !== undefined}
	onOpenChange={(open) => {
		if (!open) helpState.close();
	}}
>
	<Sheet.Content side="right" class="overflow-y-auto" closeLabel={m.help_close()}>
		<Sheet.Header>
			<Sheet.Title>{doc?.title ?? m.help_title()}</Sheet.Title>
			<Sheet.Description>
				{#if doc}<HelpInline content={doc.summary} />{:else}{m.help_missing()}{/if}
			</Sheet.Description>
		</Sheet.Header>
		{#if doc}
			<div class="px-4 pb-6"><HelpContent blocks={doc.body} /></div>
		{/if}
	</Sheet.Content>
</Sheet.Root>
