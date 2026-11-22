<script lang="ts">
	import CircleUser from '@lucide/svelte/icons/circle-user';
	import LogOut from '@lucide/svelte/icons/log-out';
	import LanguageSwitcher from '#lib/components/LanguageSwitcher.svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Popover from '#lib/components/ui/popover/index.js';
	import { m } from '#lib/paraglide/messages.js';
	import ThemeToggle from './ThemeToggle.svelte';

	/**
	 * Account menu of the header (M2-23): who is signed in, theme, language and sign-out in one
	 * place, so the header stays on one line at 1280 px with all menu items.
	 */
	let { name, onLogout }: { name: string; onLogout: () => void } = $props();
	let open = $state(false);
</script>

<Popover.Root bind:open>
	<Popover.Trigger>
		{#snippet child({ props })}
			<Button {...props} variant="ghost" size="sm">
				<CircleUser aria-hidden="true" />
				<span class="sr-only">{m.account_menu()}:</span>
				<span class="max-w-32 truncate">{name}</span>
			</Button>
		{/snippet}
	</Popover.Trigger>
	<Popover.Content
		align="end"
		role="dialog"
		aria-label={m.account_menu()}
		class="flex w-72 flex-col gap-3"
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
		<Button variant="outline" size="sm" class="self-start" onclick={onLogout}>
			<LogOut aria-hidden="true" />
			{m.logout()}
		</Button>
	</Popover.Content>
</Popover.Root>
