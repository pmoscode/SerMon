<script lang="ts">
	import CircleUser from '@lucide/svelte/icons/circle-user';
	import type { Component } from 'svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import { m } from '#lib/paraglide/messages.js';

	/**
	 * Account menu of the header (M2-23): who is signed in, theme, language and sign-out in one
	 * place, so the header stays on one line at 1280 px with all menu items. Its content loads on
	 * the first opening (M2-34, JavaScript budget of every page).
	 */
	let { name, onLogout }: { name: string; onLogout: () => void } = $props();

	type Popup = Component<
		{ name: string; anchor: HTMLElement; open: boolean; onLogout: () => void },
		object,
		'open'
	>;

	let open = $state(false);
	let anchor = $state<HTMLElement | null>(null);
	let Popup = $state<Popup | undefined>();

	async function toggle() {
		Popup ??= (await import('./AccountMenuPopup.svelte')).default as Popup;
		open = !open;
	}
</script>

<Button
	bind:ref={anchor}
	variant="ghost"
	size="sm"
	aria-haspopup="dialog"
	aria-expanded={open}
	onclick={toggle}
>
	<CircleUser aria-hidden="true" />
	<span class="sr-only">{m.account_menu()}:</span>
	<span class="max-w-32 truncate">{name}</span>
</Button>
{#if Popup && anchor}
	<Popup {name} {anchor} bind:open {onLogout} />
{/if}
