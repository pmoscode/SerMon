<script lang="ts">
	import { page } from '$app/state';
	import AuthLayout from '#lib/components/auth/AuthLayout.svelte';
	import LoginForm from '#lib/components/auth/LoginForm.svelte';
	import { redirectTo } from '#lib/navigation/navigate.js';
	import { safeRedirect } from '#lib/navigation/redirect.js';
	import { m } from '#lib/paraglide/messages.js';

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
	<LoginForm onLoggedIn={() => redirectTo(safeRedirect(page.url.searchParams.get('redirect')))} />
</AuthLayout>
