<script lang="ts">
	import LogOut from '@lucide/svelte/icons/log-out';
	import ShieldCheck from '@lucide/svelte/icons/shield-check';
	import UserRound from '@lucide/svelte/icons/user-round';
	import { resolve } from '$app/paths';
	import LanguageSwitcher from '#lib/components/LanguageSwitcher.svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Popover from '#lib/components/ui/popover/index.js';
	import { m } from '#lib/paraglide/messages.js';
	import ThemeToggle from './ThemeToggle.svelte';

	/** The content of the account menu, loaded on its first opening (M2-34). */
	let {
		name,
		anchor,
		open = $bindable(),
		onLogout
	}: { name: string; anchor: HTMLElement; open: boolean; onLogout: () => void } = $props();
</script>

<Popover.Root bind:open>
	<Popover.Content
		align="end"
		role="dialog"
		aria-label={m.account_menu()}
		class="flex w-72 flex-col gap-3"
		customAnchor={anchor}
		onInteractOutside={(event) => {
			if (anchor.contains(event.target as Node)) event.preventDefault();
		}}
		onCloseAutoFocus={(event) => {
			event.preventDefault();
			anchor.focus();
		}}
	>
		<p class="truncate text-sm text-muted-foreground" title={m.user_menu({ name })}>
			{m.user_menu({ name })}
		</p>
		<div class="flex flex-col gap-1">
			<span class="text-xs text-muted-foreground">{m.account_theme()}</span>
			<ThemeToggle />
		</div>
		<div class="flex flex-col gap-1">
			<span class="text-xs text-muted-foreground">{m.account_language()}</span>
			<LanguageSwitcher />
		</div>
		<Button
			href={resolve('/(app)/settings/account')}
			variant="ghost"
			size="sm"
			class="self-start"
			onclick={() => (open = false)}
		>
			<UserRound aria-hidden="true" />
			{m.nav_account()}
		</Button>
		<Button
			href={resolve('/(app)/settings/security')}
			variant="ghost"
			size="sm"
			class="self-start"
			onclick={() => (open = false)}
		>
			<ShieldCheck aria-hidden="true" />
			{m.nav_security()}
		</Button>
		<Button variant="outline" size="sm" class="self-start" onclick={onLogout}>
			<LogOut aria-hidden="true" />
			{m.logout()}
		</Button>
	</Popover.Content>
</Popover.Root>
