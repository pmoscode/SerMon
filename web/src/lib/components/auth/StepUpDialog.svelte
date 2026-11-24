<script lang="ts">
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Dialog from '#lib/components/ui/dialog/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { m } from '#lib/paraglide/messages.js';
	import { sessionState, type ConfirmFailure } from '#lib/stores/session.svelte.js';
	import { OidcIntent, ssoState } from '#lib/stores/sso.svelte.js';
	import { stepUpState } from '#lib/stores/stepup.svelte.js';

	/** Users with a second factor confirm with a code, the others with the password. */
	const withCode = $derived(sessionState.current?.secondFactorEnabled ?? false);
	/** Accounts from the provider may have neither: they confirm there (M3-22). */
	const local = $derived(withCode || (sessionState.current?.hasPassword ?? true));
	const withSso = $derived(ssoState.enabled && (sessionState.current?.oidcLinked ?? false));
	let leaving = $state(false);

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
				return '';
		}
	});

	function reset() {
		password = '';
		failure = undefined;
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
				>{!local
					? m.stepup_text_sso()
					: withCode
						? m.stepup_text_code()
						: m.stepup_text()}</Dialog.Description
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
