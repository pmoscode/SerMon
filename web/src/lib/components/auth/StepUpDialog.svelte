<script lang="ts">
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Dialog from '#lib/components/ui/dialog/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { m } from '#lib/paraglide/messages.js';
	import { sessionState, type ConfirmFailure } from '#lib/stores/session.svelte.js';
	import { stepUpState } from '#lib/stores/stepup.svelte.js';

	let password = $state('');
	let busy = $state(false);
	let failure = $state<ConfirmFailure | undefined>();
	let missing = $state(false);

	const failureText = $derived.by(() => {
		switch (failure) {
			case 'wrong_password':
				return m.stepup_password_wrong();
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
			failure = await sessionState.confirmPassword(password);
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
			<Dialog.Description>{m.stepup_text()}</Dialog.Description>
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
			<div class="flex flex-col gap-2">
				<Label for="stepup-password">{m.field_password_label()}</Label>
				<Input
					id="stepup-password"
					type="password"
					autocomplete="current-password"
					bind:value={password}
					aria-invalid={missing}
					aria-describedby={missing ? 'stepup-password-error' : undefined}
				/>
				{#if missing}
					<p id="stepup-password-error" class="text-sm text-destructive">{m.field_required()}</p>
				{/if}
			</div>
			<Dialog.Footer>
				<Button type="button" variant="outline" onclick={cancel}>{m.stepup_cancel()}</Button>
				<Button type="submit" disabled={busy}>{busy ? m.stepup_busy() : m.stepup_submit()}</Button>
			</Dialog.Footer>
		</form>
	</Dialog.Content>
</Dialog.Root>
