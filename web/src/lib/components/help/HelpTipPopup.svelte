<script lang="ts">
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Popover from '#lib/components/ui/popover/index.js';
	import type { HelpDoc } from '#lib/help/types.js';
	import { m } from '#lib/paraglide/messages.js';
	import HelpInline from './HelpInline.svelte';

	/**
	 * The popover of a HelpTip, loaded on its first opening (M2-34): bits-ui's floating layer
	 * stays out of the first load of every page. It hangs at the HelpTip's own button.
	 */
	let {
		doc,
		anchor,
		open = $bindable(),
		onMore
	}: { doc: HelpDoc; anchor: HTMLElement; open: boolean; onMore: () => void } = $props();
</script>

<Popover.Root bind:open>
	<Popover.Content
		class="w-80 gap-2"
		customAnchor={anchor}
		onInteractOutside={(event) => {
			// The button toggles itself; a click on it is no click outside.
			if (anchor.contains(event.target as Node)) event.preventDefault();
		}}
		onCloseAutoFocus={(event) => {
			event.preventDefault();
			anchor.focus();
		}}
	>
		<p class="font-medium">{doc.title}</p>
		<p class="text-muted-foreground"><HelpInline content={doc.summary} /></p>
		{#if doc.body.length || doc.more}
			<Button variant="link" size="sm" class="self-start px-0" onclick={onMore}
				>{m.help_more()}</Button
			>
		{/if}
	</Popover.Content>
</Popover.Root>
