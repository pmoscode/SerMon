<script lang="ts">
	import { api } from '#lib/api/client.js';
	import AuthLayout from '#lib/components/auth/AuthLayout.svelte';
	import SetupForm from '#lib/components/auth/SetupForm.svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import { redirectTo } from '#lib/navigation/navigate.js';
	import { loginPath } from '#lib/navigation/redirect.js';
	import { m } from '#lib/paraglide/messages.js';

	type State = 'loading' | 'required' | 'done' | 'unavailable';
	let state = $state<State>('loading');

	async function check() {
		state = 'loading';
		try {
			const res = await api.setup.getSetupStatus({});
			state = res.setupRequired ? 'required' : 'done';
		} catch {
			state = 'unavailable';
		}
	}

	$effect(() => {
		void check();
	});
</script>

<svelte:head>
	<title>{m.setup_title()} · SerMon</title>
</svelte:head>

<AuthLayout title={m.setup_title()} description={m.setup_subtitle()}>
	{#if state === 'loading'}
		<p role="status" class="text-muted-foreground">{m.loading()}</p>
	{:else if state === 'unavailable'}
		<div class="flex flex-col items-start gap-3">
			<p role="alert" class="text-sm text-destructive">{m.error_unavailable()}</p>
			<Button variant="outline" onclick={check}>{m.retry()}</Button>
		</div>
	{:else if state === 'done'}
		<div class="flex flex-col items-start gap-3">
			<p role="status">{m.setup_not_required()}</p>
			<Button href="/login">{m.setup_to_login()}</Button>
		</div>
	{:else}
		<SetupForm onDone={() => redirectTo(loginPath('setup_done'))} />
	{/if}
</AuthLayout>
