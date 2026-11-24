<script lang="ts">
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { m } from '#lib/paraglide/messages.js';
	import {
		sessionState,
		type LoginFailure,
		type SecondFactorFailure
	} from '#lib/stores/session.svelte.js';
	import { OidcIntent, ssoState, type SsoFailure } from '#lib/stores/sso.svelte.js';

	let {
		onLoggedIn,
		initialStep = 'password',
		next = ''
	}: {
		onLoggedIn: () => void;
		/** code: the provider signed in, the local second factor follows (M3-22). */
		initialStep?: 'password' | 'code';
		/** Where the sign-in at the provider returns to. */
		next?: string;
	} = $props();

	/** password: e-mail and password; code: the second factor (TOTP or recovery code). */
	// svelte-ignore state_referenced_locally
	let step = $state<'password' | 'code'>(initialStep);
	let ssoFailure = $state<SsoFailure | undefined>();
	let leaving = $state(false);

	async function signInWithSso() {
		ssoFailure = undefined;
		failure = undefined;
		leaving = true;
		ssoFailure = await ssoState.start(OidcIntent.LOGIN, next);
		if (ssoFailure) leaving = false;
	}
	let email = $state('');
	let password = $state('');
	let code = $state('');
	let submitted = $state(false);
	let busy = $state(false);
	let failure = $state<LoginFailure | SecondFactorFailure | undefined>();

	const emailMissing = $derived(submitted && step === 'password' && !email.trim());
	const passwordMissing = $derived(submitted && step === 'password' && !password);
	const codeMissing = $derived(submitted && step === 'code' && !code.trim());

	const failureText = $derived.by(() => {
		switch (failure) {
			case 'credentials':
				return m.login_error_credentials();
			case 'wrong_code':
				return m.login_error_code();
			case 'expired':
				return m.login_error_code_expired();
			case 'rate_limited':
				return m.login_error_rate_limited();
			case 'unavailable':
				return m.error_unavailable();
			default:
				break;
		}
		switch (ssoFailure) {
			case 'rate_limited':
				return m.login_error_rate_limited();
			case undefined:
				return '';
			default:
				return m.sso_error_unavailable();
		}
	});

	async function submitPassword() {
		if (!email.trim() || !password) return;
		busy = true;
		try {
			const result = await sessionState.login(email.trim(), password);
			if (result === 'second_factor') {
				password = '';
				submitted = false;
				step = 'code';
				return;
			}
			failure = result;
		} finally {
			busy = false;
		}
		if (!failure) {
			password = '';
			onLoggedIn();
		}
	}

	async function submitCode() {
		if (!code.trim()) return;
		busy = true;
		try {
			failure = await sessionState.verifySecondFactor(code.trim());
		} finally {
			busy = false;
		}
		code = '';
		if (failure === 'expired') {
			step = 'password';
			submitted = false;
		} else if (!failure) {
			onLoggedIn();
		}
	}

	async function submit(event: SubmitEvent) {
		event.preventDefault();
		submitted = true;
		failure = undefined;
		await (step === 'password' ? submitPassword() : submitCode());
	}

	function back() {
		step = 'password';
		code = '';
		submitted = false;
		failure = undefined;
	}
</script>

<form class="flex flex-col gap-4" novalidate onsubmit={submit}>
	{#if failureText}
		<p role="alert" class="rounded-md border border-destructive/50 p-3 text-sm text-destructive">
			{failureText}
		</p>
	{/if}
	{#if step === 'password'}
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
		{#if ssoState.enabled}
			<p class="flex items-center gap-2 text-xs text-muted-foreground" aria-hidden="true">
				<span class="h-px flex-1 bg-border"></span>{m.login_or()}<span class="h-px flex-1 bg-border"
				></span>
			</p>
			<Button type="button" variant="outline" disabled={leaving} onclick={signInWithSso}>
				{leaving ? m.login_busy() : m.sso_sign_in({ name: ssoState.name })}
			</Button>
		{/if}
	{:else}
		<div class="flex flex-col gap-2">
			<Label for="login-code">{m.field_code()}</Label>
			<Input
				id="login-code"
				type="text"
				inputmode="text"
				autocomplete="one-time-code"
				autofocus
				bind:value={code}
				aria-invalid={codeMissing}
				aria-describedby={codeMissing ? 'login-code-error' : 'login-code-hint'}
			/>
			{#if codeMissing}
				<p id="login-code-error" class="text-sm text-destructive">{m.field_required()}</p>
			{:else}
				<p id="login-code-hint" class="text-sm text-muted-foreground">{m.login_code_hint()}</p>
			{/if}
		</div>
		<div class="flex gap-2">
			<Button type="button" variant="outline" onclick={back}>{m.back()}</Button>
			<Button type="submit" class="flex-1" disabled={busy}>
				{busy ? m.login_busy() : m.login_code_submit()}
			</Button>
		</div>
	{/if}
</form>
