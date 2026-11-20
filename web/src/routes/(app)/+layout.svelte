<script lang="ts">
	import { page } from '$app/state';
	import AppShell from '#lib/components/shell/AppShell.svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import { redirectTo } from '#lib/navigation/navigate.js';
	import { loginPath } from '#lib/navigation/redirect.js';
	import { m } from '#lib/paraglide/messages.js';
	import { sessionState } from '#lib/stores/session.svelte.js';
	import type { LayoutProps } from './$types';

	let { children }: LayoutProps = $props();

	let unavailable = $state(false);
	let logoutFailed = $state(false);

	async function check() {
		unavailable = false;
		try {
			await sessionState.load();
		} catch {
			unavailable = true;
		}
	}

	// The session cookie is checked once; afterwards any call can end it (expiry interceptor).
	$effect(() => {
		if (sessionState.status === 'unknown') void check();
	});

	$effect(() => {
		const status = sessionState.status;
		if (status === 'anonymous' || status === 'expired') {
			const from = page.url.pathname + page.url.search;
			void redirectTo(loginPath(status === 'expired' ? 'expired' : undefined, from));
		}
	});

	// While the organization requires a second factor the user lacks, only its setup works.
	$effect(() => {
		if (sessionState.secondFactorSetupRequired && page.route.id !== '/(app)/settings/security') {
			void redirectTo('/settings/security');
		}
	});

	async function logout() {
		logoutFailed = false;
		try {
			await sessionState.logout();
			await redirectTo(loginPath('logged_out'));
		} catch {
			logoutFailed = true;
		}
	}
</script>

{#if sessionState.status === 'authenticated'}
	<AppShell onLogout={logout}>
		{#if logoutFailed}
			<p role="alert" class="mb-4 text-sm text-destructive">{m.logout_failed()}</p>
		{/if}
		{#if sessionState.secondFactorSetupRequired && page.route.id !== '/(app)/settings/security'}
			<p role="status" class="text-muted-foreground">{m.loading()}</p>
		{:else}
			{#key sessionState.current?.activeOrganizationId}
				{@render children()}
			{/key}
		{/if}
	</AppShell>
{:else if unavailable}
	<main id="content" class="flex flex-col items-start gap-3 p-8">
		<p role="alert" class="text-destructive">{m.error_unavailable()}</p>
		<Button variant="outline" onclick={check}>{m.retry()}</Button>
	</main>
{:else}
	<p role="status" class="p-8 text-muted-foreground">{m.loading()}</p>
{/if}
