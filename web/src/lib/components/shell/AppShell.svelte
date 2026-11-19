<script lang="ts">
	import LogOut from '@lucide/svelte/icons/log-out';
	import Search from '@lucide/svelte/icons/search';
	import Server from '@lucide/svelte/icons/server';
	import type { Snippet } from 'svelte';
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import LanguageSwitcher from '#lib/components/LanguageSwitcher.svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import { m } from '#lib/paraglide/messages.js';
	import { sessionState } from '#lib/stores/session.svelte.js';
	import OrgSwitcher from './OrgSwitcher.svelte';
	import ThemeToggle from './ThemeToggle.svelte';

	let { children, onLogout }: { children: Snippet; onLogout: () => void } = $props();

	const nav = [{ href: resolve('/(app)/fleet'), icon: Server, label: () => m.nav_fleet() }];
	const name = $derived(sessionState.current?.displayName || sessionState.current?.email || '');
</script>

<a
	href="#content"
	class="sr-only z-50 rounded-md bg-background px-3 py-2 focus:not-sr-only focus:fixed focus:top-2 focus:left-2"
>
	{m.shell_skip_to_content()}
</a>

<div class="flex min-h-screen flex-col">
	<header class="border-b">
		<div class="mx-auto flex max-w-7xl flex-wrap items-center gap-x-4 gap-y-2 px-4 py-2">
			<a href={resolve('/(app)/fleet')} class="font-semibold tracking-tight">SerMon</a>
			<nav aria-label={m.shell_navigation()} class="flex gap-1">
				{#each nav as item (item.href)}
					{@const current = page.url.pathname.startsWith(item.href)}
					<Button
						href={item.href}
						variant={current ? 'secondary' : 'ghost'}
						size="sm"
						aria-current={current ? 'page' : undefined}
					>
						<item.icon aria-hidden="true" />
						{item.label()}
					</Button>
				{/each}
			</nav>
			<OrgSwitcher />
			<div class="ml-auto flex flex-wrap items-center gap-2">
				<Button variant="outline" size="sm" disabled title={m.command_palette_soon()}>
					<Search aria-hidden="true" />
					{m.command_palette()}
					<kbd class="font-mono text-xs text-muted-foreground">⌘K</kbd>
				</Button>
				<ThemeToggle />
				<LanguageSwitcher />
				<span class="max-w-40 truncate text-sm text-muted-foreground" title={m.user_menu({ name })}>
					{name}
				</span>
				<Button variant="ghost" size="sm" onclick={onLogout}>
					<LogOut aria-hidden="true" />
					{m.logout()}
				</Button>
			</div>
		</div>
	</header>
	<main id="content" tabindex="-1" class="mx-auto w-full max-w-7xl flex-1 px-4 py-6">
		{@render children()}
	</main>
</div>
