<script lang="ts">
	import Info from '@lucide/svelte/icons/info';
	import type { Component } from 'svelte';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import type { HelpDoc } from '#lib/help/types.js';
	import { m } from '#lib/paraglide/messages.js';
	import { helpState } from '#lib/stores/help.svelte.js';

	/**
	 * ⓘ next to a metric or key figure: the summary of a help topic, more in the help panel. The
	 * popover loads on the first opening (M2-34, JavaScript budget of every page).
	 */
	let { topic }: { topic: string } = $props();

	type Popup = Component<
		{ doc: HelpDoc; anchor: HTMLElement; open: boolean; onMore: () => void },
		object,
		'open'
	>;

	let open = $state(false);
	let anchor = $state<HTMLButtonElement | null>(null);
	let Popup = $state<Popup | undefined>();
	const doc = $derived(helpState.get(topic));

	$effect(() => {
		void helpState.loadSummaries(localeState.current);
	});

	async function toggle() {
		Popup ??= (await import('./HelpTipPopup.svelte')).default as Popup;
		open = !open;
	}

	function more() {
		open = false;
		helpState.open(topic);
	}
</script>

{#if doc}
	<button
		bind:this={anchor}
		type="button"
		class="inline-flex size-6 items-center justify-center rounded-full text-muted-foreground hover:text-foreground focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
		aria-label={m.help_about({ title: doc.title })}
		aria-haspopup="dialog"
		aria-expanded={open}
		onclick={toggle}
	>
		<Info class="size-4" aria-hidden="true" />
	</button>
	{#if Popup && anchor}
		<Popup {doc} {anchor} bind:open onMore={more} />
	{/if}
{/if}
