<script lang="ts">
	import { page } from '$app/state';
	import AuthLayout from '#lib/components/auth/AuthLayout.svelte';
	import LoginForm from '#lib/components/auth/LoginForm.svelte';
	import { redirectTo } from '#lib/navigation/navigate.js';
	import { safeRedirect } from '#lib/navigation/redirect.js';
	import { m } from '#lib/paraglide/messages.js';
	import { ssoState } from '#lib/stores/sso.svelte.js';

	$effect(() => {
		void ssoState.load();
	});

	const sso = $derived(page.url.searchParams.get('sso'));
	const notice = $derived.by(() => {
		switch (page.url.searchParams.get('reason')) {
			case 'expired':
				return m.login_expired();
			case 'logged_out':
				return m.login_logged_out();
			case 'setup_done':
				return m.login_setup_done();
			case 'invited':
				return m.login_invited();
			default:
				return '';
		}
	});
</script>

<svelte:head>
	<title>{m.login_title()} · SerMon</title>
</svelte:head>

<AuthLayout title={m.login_title()} description={m.login_subtitle()}>
	{#if notice}
		<p role="status" class="mb-4 rounded-md border bg-muted p-3 text-sm">{notice}</p>
	{/if}
	{#if sso === 'failed'}
		<!-- Without details: the reason is in the server log (no hints about accounts). -->
		<p
			role="alert"
			class="mb-4 rounded-md border border-destructive/50 p-3 text-sm text-destructive"
		>
			{m.sso_failed()}
		</p>
	{:else if sso === 'second_factor'}
		<p role="status" class="mb-4 rounded-md border bg-muted p-3 text-sm">{m.sso_second_factor()}</p>
	{/if}
	<LoginForm
		initialStep={sso === 'second_factor' ? 'code' : 'password'}
		next={page.url.searchParams.get('redirect') ?? ''}
		onLoggedIn={() => redirectTo(safeRedirect(page.url.searchParams.get('redirect')))}
	/>
</AuthLayout>
