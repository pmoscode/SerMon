<script lang="ts">
	import Bell from '@lucide/svelte/icons/bell';
	import Building2 from '@lucide/svelte/icons/building-2';
	import CircleQuestionMark from '@lucide/svelte/icons/circle-question-mark';
	import Megaphone from '@lucide/svelte/icons/megaphone';
	import PackageCheck from '@lucide/svelte/icons/package-check';
	import ScrollText from '@lucide/svelte/icons/scroll-text';
	import Search from '@lucide/svelte/icons/search';
	import Server from '@lucide/svelte/icons/server';
	import ShieldAlert from '@lucide/svelte/icons/shield-alert';
	import Users from '@lucide/svelte/icons/users';
	import type { Snippet } from 'svelte';
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import StepUpDialog from '#lib/components/auth/StepUpDialog.svelte';
	import HelpPanel from '#lib/components/help/HelpPanel.svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import { pageHelp } from '#lib/help/pages.js';
	import { m } from '#lib/paraglide/messages.js';
	import { helpState } from '#lib/stores/help.svelte.js';
	import { sessionState } from '#lib/stores/session.svelte.js';
	import OrgSwitcher from './OrgSwitcher.svelte';
	import AccountMenu from './AccountMenu.svelte';
	import InstanceHealth from './InstanceHealth.svelte';

	let { children, onLogout }: { children: Snippet; onLogout: () => void } = $props();

	const nav = $derived([
		// The fleet's dashboards (anomalies, M4-25) are part of it.
		{
			href: resolve('/(app)/fleet'),
			icon: Server,
			label: () => m.nav_fleet(),
			also: [resolve('/(app)/capacity'), resolve('/(app)/heatmap'), resolve('/(app)/anomalies')]
		},
		{ href: resolve('/(app)/alerts'), icon: Bell, label: () => m.nav_alerts() },
		{ href: resolve('/(app)/logs'), icon: ScrollText, label: () => m.nav_logs() },
		{ href: resolve('/(app)/security'), icon: ShieldAlert, label: () => m.nav_security_events() },
		{ href: resolve('/(app)/updates'), icon: PackageCheck, label: () => m.nav_updates() },
		...(sessionState.current?.orgAdmin
			? [
					{
						href: resolve('/(app)/notifications'),
						icon: Megaphone,
						label: () => m.nav_notifications()
					},
					{ href: resolve('/(app)/admin'), icon: Users, label: () => m.nav_admin() }
				]
			: []),
		...(sessionState.current?.superadmin
			? [
					{
						href: resolve('/(app)/organizations'),
						icon: Building2,
						label: () => m.nav_organizations()
					}
				]
			: [])
	]);
	const name = $derived(sessionState.current?.displayName || sessionState.current?.email || '');
	const helpTopic = $derived(page.route.id ? pageHelp[page.route.id] : undefined);

	/** "?" opens the help of the page (KONZEPT §17), except while typing. */
	function onKeydown(event: KeyboardEvent) {
		if (event.key !== '?' || event.ctrlKey || event.metaKey || event.altKey || !helpTopic) return;
		const target = event.target as HTMLElement | null;
		if (
			target?.closest('input, textarea, select, [contenteditable]:not([contenteditable="false"])')
		)
			return;
		event.preventDefault();
		helpState.open(helpTopic);
	}
</script>

<svelte:window onkeydown={onKeydown} />

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
					{@const current = [item.href, ...('also' in item ? (item.also ?? []) : [])].some((h) =>
						page.url.pathname.startsWith(h)
					)}
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
					<span class="sr-only">{m.command_palette()}</span>
					<kbd class="font-mono text-xs text-muted-foreground">⌘K</kbd>
				</Button>
				{#if helpTopic}
					<Button
						variant="ghost"
						size="icon-sm"
						aria-label={m.help_page()}
						title="{m.help_page()} (?)"
						aria-keyshortcuts="?"
						onclick={() => helpState.open(helpTopic)}
					>
						<CircleQuestionMark aria-hidden="true" />
					</Button>
				{/if}
				<AccountMenu {name} {onLogout} />
			</div>
		</div>
	</header>
	<InstanceHealth />
	<main id="content" tabindex="-1" class="mx-auto w-full max-w-7xl flex-1 px-4 py-6">
		{@render children()}
	</main>
</div>

<HelpPanel />
<StepUpDialog />
