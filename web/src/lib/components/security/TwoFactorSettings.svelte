<script lang="ts">
	import ShieldCheck from '@lucide/svelte/icons/shield-check';
	import ShieldAlert from '@lucide/svelte/icons/shield-exclamation-point';
	import { Code, codeOf } from '#lib/api/client.js';
	import { adminApi } from '#lib/api/admin.js';
	import type { GetTwoFactorStatusResponse } from '#lib/api/gen/sermon/web/v1/two_factor_pb.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Card from '#lib/components/ui/card/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { m } from '#lib/paraglide/messages.js';
	import { sessionState } from '#lib/stores/session.svelte.js';
	import { needsStepUp, withStepUp } from '#lib/stores/stepup.svelte.js';
	import QrCode from './QrCode.svelte';
	import RecoveryCodes from './RecoveryCodes.svelte';

	type View =
		| { kind: 'loading' }
		| { kind: 'unavailable' }
		| { kind: 'status'; status: GetTwoFactorStatusResponse }
		| { kind: 'setup'; secret: string; uri: string }
		| { kind: 'codes'; codes: string[] };

	let view = $state<View>({ kind: 'loading' });
	let code = $state('');
	let busy = $state(false);
	let failure = $state('');
	let confirmDisable = $state(false);

	async function load() {
		failure = '';
		confirmDisable = false;
		try {
			view = { kind: 'status', status: await adminApi.twoFactor.getTwoFactorStatus({}) };
		} catch {
			view = { kind: 'unavailable' };
		}
	}

	$effect(() => {
		void load();
	});

	/** Runs a sensitive action; a cancelled step-up is no failure. */
	async function run(action: () => Promise<void>) {
		busy = true;
		failure = '';
		try {
			await withStepUp(action);
		} catch (error) {
			if (!needsStepUp(error)) failure = m.error_unavailable();
		} finally {
			busy = false;
		}
	}

	const begin = () =>
		run(async () => {
			const res = await adminApi.twoFactor.beginTotpSetup({});
			code = '';
			view = { kind: 'setup', secret: res.secret, uri: res.uri };
		});

	async function confirm(event: SubmitEvent) {
		event.preventDefault();
		if (!code.trim()) return;
		busy = true;
		failure = '';
		try {
			const res = await adminApi.twoFactor.confirmTotpSetup({ code: code.trim() });
			view = { kind: 'codes', codes: res.recoveryCodes };
		} catch (error) {
			failure =
				codeOf(error) === Code.PermissionDenied || codeOf(error) === Code.InvalidArgument
					? m.security_setup_wrong_code()
					: codeOf(error) === Code.ResourceExhausted
						? m.login_error_rate_limited()
						: m.error_unavailable();
		} finally {
			busy = false;
		}
	}

	const regenerate = () =>
		run(async () => {
			const res = await adminApi.twoFactor.regenerateRecoveryCodes({});
			view = { kind: 'codes', codes: res.recoveryCodes };
		});

	const disable = () =>
		run(async () => {
			await adminApi.twoFactor.disableTwoFactor({});
			await sessionState.refresh();
			await load();
		});

	async function finish() {
		await sessionState.refresh().catch(() => {});
		await load();
	}
</script>

<Card.Root class="max-w-2xl">
	<Card.Header>
		<Card.Title>
			<h2 class="text-lg font-medium">{m.security_2fa_title()}</h2>
		</Card.Title>
		<Card.Description>{m.security_2fa_text()}</Card.Description>
	</Card.Header>
	<Card.Content class="flex flex-col gap-4">
		{#if failure}
			<p role="alert" class="rounded-md border border-destructive/50 p-3 text-sm text-destructive">
				{failure}
			</p>
		{/if}
		{#if view.kind === 'loading'}
			<p role="status" class="text-muted-foreground">{m.loading()}</p>
		{:else if view.kind === 'unavailable'}
			<div class="flex flex-col items-start gap-3">
				<p role="alert" class="text-sm text-destructive">{m.error_unavailable()}</p>
				<Button variant="outline" onclick={load}>{m.retry()}</Button>
			</div>
		{:else if view.kind === 'status'}
			{@const status = view.status}
			{#if status.totpEnabled}
				<p class="flex items-center gap-2 text-sm">
					<ShieldCheck class="size-5 text-emerald-600 dark:text-emerald-400" aria-hidden="true" />
					{m.security_2fa_on({ left: String(status.recoveryCodesLeft) })}
				</p>
				<div class="flex flex-wrap gap-2">
					<Button variant="outline" disabled={busy} onclick={regenerate}
						>{m.security_codes_new()}</Button
					>
					{#if confirmDisable}
						<Button variant="destructive" disabled={busy} onclick={disable}>
							{m.security_disable_confirm()}
						</Button>
						<Button variant="ghost" onclick={() => (confirmDisable = false)}
							>{m.stepup_cancel()}</Button
						>
					{:else}
						<Button variant="ghost" onclick={() => (confirmDisable = true)}
							>{m.security_disable()}</Button
						>
					{/if}
				</div>
				{#if confirmDisable && status.requiredByOrganization}
					<p role="alert" class="text-sm text-destructive">{m.security_disable_required()}</p>
				{/if}
			{:else}
				<p class="flex items-center gap-2 text-sm">
					<ShieldAlert class="size-5 text-amber-600 dark:text-amber-400" aria-hidden="true" />
					{status.requiredByOrganization ? m.security_2fa_required() : m.security_2fa_off()}
				</p>
				<Button class="self-start" disabled={busy} onclick={begin}
					>{m.security_setup_start()}</Button
				>
			{/if}
		{:else if view.kind === 'setup'}
			<ol class="flex list-decimal flex-col gap-4 pl-5 text-sm">
				<li class="flex flex-col gap-3">
					{m.security_setup_scan()}
					<QrCode value={view.uri} label={m.security_setup_qr()} />
					<details>
						<summary class="cursor-pointer text-muted-foreground"
							>{m.security_setup_manual()}</summary
						>
						<code class="mt-2 block font-mono text-sm break-all select-all">{view.secret}</code>
					</details>
				</li>
				<li>
					<form class="flex flex-col gap-2" novalidate onsubmit={confirm}>
						<Label for="totp-code">{m.security_setup_code()}</Label>
						<div class="flex flex-wrap gap-2">
							<Input
								id="totp-code"
								class="max-w-40 font-mono"
								inputmode="numeric"
								autocomplete="one-time-code"
								bind:value={code}
							/>
							<Button type="submit" disabled={busy}>{m.security_setup_confirm()}</Button>
							<Button type="button" variant="ghost" onclick={load}>{m.stepup_cancel()}</Button>
						</div>
					</form>
				</li>
			</ol>
		{:else}
			<RecoveryCodes codes={view.codes} onDone={finish} />
		{/if}
	</Card.Content>
</Card.Root>
