<script lang="ts">
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import { Button } from '#lib/components/ui/button/index.js';
	import { m } from '#lib/paraglide/messages.js';
	import { sessionState } from '#lib/stores/session.svelte.js';

	/** The superadmin's pages of the instance (V1-19): one item in the header, tabs here. */
	const tabs = [
		{ href: resolve('/(app)/organizations'), label: () => m.nav_organizations() },
		{ href: resolve('/(app)/agent-ca'), label: () => m.nav_agent_ca() }
	];
</script>

{#if sessionState.current?.superadmin}
	<nav aria-label={m.instance_pages()} class="mb-6 flex gap-1 border-b pb-2">
		{#each tabs as tab (tab.href)}
			{@const current = page.url.pathname.startsWith(tab.href)}
			<Button
				href={tab.href}
				size="sm"
				variant={current ? 'secondary' : 'ghost'}
				aria-current={current ? 'page' : undefined}
			>
				{tab.label()}
			</Button>
		{/each}
	</nav>
{/if}
