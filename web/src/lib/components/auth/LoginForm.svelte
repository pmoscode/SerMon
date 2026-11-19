<script lang="ts">
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { m } from '#lib/paraglide/messages.js';
	import { sessionState, type LoginFailure } from '#lib/stores/session.svelte.js';

	let { onLoggedIn }: { onLoggedIn: () => void } = $props();

	let email = $state('');
	let password = $state('');
	let submitted = $state(false);
	let busy = $state(false);
	let failure = $state<LoginFailure | undefined>();

	const emailMissing = $derived(submitted && !email.trim());
	const passwordMissing = $derived(submitted && !password);

	const failureText = $derived.by(() => {
		switch (failure) {
			case 'credentials':
				return m.login_error_credentials();
			case 'rate_limited':
				return m.login_error_rate_limited();
			case 'unavailable':
				return m.error_unavailable();
			default:
				return '';
		}
	});

	async function submit(event: SubmitEvent) {
		event.preventDefault();
		submitted = true;
		failure = undefined;
		if (!email.trim() || !password) return;
		busy = true;
		try {
			failure = await sessionState.login(email.trim(), password);
		} finally {
			busy = false;
		}
		if (!failure) {
			password = '';
			onLoggedIn();
		}
	}
</script>

<form class="flex flex-col gap-4" novalidate onsubmit={submit}>
	{#if failureText}
		<p role="alert" class="rounded-md border border-destructive/50 p-3 text-sm text-destructive">
			{failureText}
		</p>
	{/if}
	<div class="flex flex-col gap-2">
		<Label for="login-email">{m.field_email()}</Label>
		<Input
			id="login-email"
			type="email"
			autocomplete="username"
			bind:value={email}
			aria-invalid={emailMissing}
			aria-describedby={emailMissing ? 'login-email-error' : undefined}
		/>
		{#if emailMissing}
			<p id="login-email-error" class="text-sm text-destructive">{m.field_required()}</p>
		{/if}
	</div>
	<div class="flex flex-col gap-2">
		<Label for="login-password">{m.field_password_label()}</Label>
		<Input
			id="login-password"
			type="password"
			autocomplete="current-password"
			bind:value={password}
			aria-invalid={passwordMissing}
			aria-describedby={passwordMissing ? 'login-password-error' : undefined}
		/>
		{#if passwordMissing}
			<p id="login-password-error" class="text-sm text-destructive">{m.field_required()}</p>
		{/if}
	</div>
	<Button type="submit" disabled={busy}>{busy ? m.login_busy() : m.login_submit()}</Button>
</form>
