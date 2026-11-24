<script lang="ts">
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Dialog from '#lib/components/ui/dialog/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { m } from '#lib/paraglide/messages.js';
	import { sessionState, type ConfirmFailure } from '#lib/stores/session.svelte.js';
	import { supported } from '#lib/auth/webauthn.js';
	import { assertPasskey, PasskeyPurpose, type PasskeyFailure } from '#lib/stores/passkeys.js';
	import { OidcIntent, ssoState } from '#lib/stores/sso.svelte.js';
	import { stepUpState } from '#lib/stores/stepup.svelte.js';

	/**
	 * Users with TOTP confirm with a code, users without a second factor with the password; a
	 * second factor replaces the password (M1-03). Passkeys (M3-24) and the provider (M3-22) are
	 * ways of their own; accounts from the provider may have nothing else.
	 */
	const current = $derived(sessionState.current);
	const withCode = $derived(current?.totpEnabled ?? false);
	const withPassword = $derived(
		!(current?.secondFactorEnabled ?? false) && (current?.hasPassword ?? true)
	);
	const local = $derived(withCode || withPassword);
	const withPasskey = $derived(
		(current?.passkeysAvailable ?? false) && (current?.passkeyCount ?? 0) > 0 && supported()
	);
	// With a second factor, the provider's login counts only if the operator trusts it as two
	// factors; the server refuses the others (M3-28).
	const withSso = $derived(
		ssoState.enabled &&
			(current?.oidcLinked ?? false) &&
			(!(current?.secondFactorEnabled ?? false) || ssoState.secondFactor)
	);
	let leaving = $state(false);
	let checking = $state(false);
	let passkeyFailure = $state<PasskeyFailure | undefined>();

	/** Confirms with a passkey right here; the action runs on. */
	async function confirmWithPasskey() {
		failure = undefined;
		passkeyFailure = undefined;
		checking = true;
		try {
			passkeyFailure = await assertPasskey(PasskeyPurpose.STEP_UP);
		} finally {
			checking = false;
		}
		if (!passkeyFailure) {
			reset();
			stepUpState.finish(true);
		} else if (passkeyFailure === 'cancelled') {
			passkeyFailure = undefined;
		}
	}

	$effect(() => {
		if (stepUpState.open) void ssoState.load();
	});

	/** A fresh sign-in at the provider; the browser returns to this page, the action runs again by hand. */
	async function confirmWithSso() {
		failure = undefined;
		leaving = true;
		const result = await ssoState.start(
			OidcIntent.STEP_UP,
			window.location.pathname + window.location.search
		);
		if (result) {
			leaving = false;
			failure = result === 'rate_limited' ? 'rate_limited' : 'unavailable';
		}
	}
	let password = $state('');
	let busy = $state(false);
	let failure = $state<ConfirmFailure | undefined>();
	let missing = $state(false);

	const failureText = $derived.by(() => {
		switch (failure) {
			case 'wrong_password':
				return m.stepup_password_wrong();
			case 'wrong_code':
				return m.login_error_code();
			case 'rate_limited':
				return m.login_error_rate_limited();
			case 'unavailable':
				return m.error_unavailable();
			default:
				break;
		}
		switch (passkeyFailure) {
			case 'refused':
				return m.passkey_refused();
			case 'unsupported':
				return m.passkey_unsupported();
			case 'rate_limited':
				return m.login_error_rate_limited();
			case undefined:
			case 'cancelled':
				return '';
			default:
				return m.error_unavailable();
		}
	});

	function reset() {
		password = '';
		failure = undefined;
		passkeyFailure = undefined;
		missing = false;
	}

	function cancel() {
		reset();
		stepUpState.finish(false);
	}

	async function submit(event: SubmitEvent) {
		event.preventDefault();
		failure = undefined;
		missing = !password;
		if (missing) return;
		busy = true;
		try {
			failure = withCode
				? await sessionState.confirmSecondFactor(password.trim())
				: await sessionState.confirmPassword(password);
		} finally {
			busy = false;
		}
		if (!failure) {
			reset();
			stepUpState.finish(true);
		}
	}
</script>

<Dialog.Root
	open={stepUpState.open}
	onOpenChange={(open) => {
		if (!open) cancel();
	}}
>
	<Dialog.Content closeLabel={m.stepup_cancel()} class="sm:max-w-md">
		<Dialog.Header>
			<Dialog.Title>{m.stepup_title()}</Dialog.Title>
			<Dialog.Description
				>{local
					? withCode
						? m.stepup_text_code()
						: m.stepup_text()
					: withPasskey
						? m.stepup_text_passkey()
						: m.stepup_text_sso()}</Dialog.Description
			>
		</Dialog.Header>
		<form class="flex flex-col gap-4" novalidate onsubmit={submit}>
			{#if failureText}
				<p
					role="alert"
					class="rounded-md border border-destructive/50 p-3 text-sm text-destructive"
				>
					{failureText}
				</p>
			{/if}
			{#if local}
				<div class="flex flex-col gap-2">
					<Label for="stepup-password">{withCode ? m.field_code() : m.field_password_label()}</Label
					>
					<Input
						id="stepup-password"
						type={withCode ? 'text' : 'password'}
						autocomplete={withCode ? 'one-time-code' : 'current-password'}
						bind:value={password}
						aria-invalid={missing}
						aria-describedby={missing ? 'stepup-password-error' : undefined}
					/>
					{#if missing}
						<p id="stepup-password-error" class="text-sm text-destructive">{m.field_required()}</p>
					{/if}
				</div>
			{/if}
			{#if withPasskey}
				<Button type="button" variant="outline" disabled={checking} onclick={confirmWithPasskey}>
					{checking ? m.stepup_busy() : m.stepup_passkey()}
				</Button>
			{/if}
			{#if withSso}
				<Button type="button" variant="outline" disabled={leaving} onclick={confirmWithSso}>
					{leaving ? m.stepup_busy() : m.stepup_sso({ name: ssoState.name })}
				</Button>
				<p class="text-xs text-muted-foreground">{m.stepup_sso_hint()}</p>
			{/if}
			<Dialog.Footer>
				<Button type="button" variant="outline" onclick={cancel}>{m.stepup_cancel()}</Button>
				{#if local}
					<Button type="submit" disabled={busy}>{busy ? m.stepup_busy() : m.stepup_submit()}</Button
					>
				{/if}
			</Dialog.Footer>
		</form>
	</Dialog.Content>
</Dialog.Root>
