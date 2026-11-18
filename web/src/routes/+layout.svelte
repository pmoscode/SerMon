<script lang="ts">
	import './layout.css';
	import favicon from '#lib/assets/favicon.svg';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import type { LayoutProps } from './$types';

	let { children }: LayoutProps = $props();

	$effect(() => {
		document.documentElement.lang = localeState.current;
	});
</script>

<svelte:head>
	<link rel="icon" href={favicon} />
</svelte:head>

{#key localeState.current}
	{@render children()}
{/key}
