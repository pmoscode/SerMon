<script lang="ts">
	import Download from '@lucide/svelte/icons/download';
	import UserRound from '@lucide/svelte/icons/user-round';
	import { timestampDate } from '@bufbuild/protobuf/wkt';
	import { Code, codeOf } from '#lib/api/client.js';
	import { accountApi } from '#lib/api/account.js';
	import type { GetAccountResponse } from '#lib/api/gen/sermon/web/v1/account_pb.js';
	import { saveFile } from '#lib/download/download.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Card from '#lib/components/ui/card/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { formatTimestamp } from '#lib/format/format.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { m } from '#lib/paraglide/messages.js';
	import { sessionState } from '#lib/stores/session.svelte.js';
	import { needsStepUp, withStepUp } from '#lib/stores/stepup.svelte.js';

	/**
	 * The own account (V1-17): name and e-mail address, each after a step-up; a new address with
	 * the code mailed to it (without SMTP it applies at once); the export of the account's data as
	 * a JSON file. Addresses and names are shown as text.
	 */
	let account = $state<GetAccountResponse | undefined>();
	let status = $state<'loading' | 'ready' | 'error'>('loading');
	let name = $state('');
	let email = $state('');
	let code = $state('');
	let busy = $state(false);
	let failure = $state('');
	let notice = $state('');
	const locale = $derived(localeState.current);

	async function load() {
		try {
			account = await accountApi.account.getAccount({});
			name = account.displayName;
			status = 'ready';
		} catch (error) {
			if (codeOf(error) !== Code.Unauthenticated) status = 'error';
		}
	}

	$effect(() => {
		void load();
	});

	function message(error: unknown): string {
		if (needsStepUp(error)) return '';
		switch (codeOf(error)) {
			case Code.InvalidArgument:
				return m.account_error_invalid();
			case Code.AlreadyExists:
				return m.account_error_taken();
			case Code.PermissionDenied:
				return m.account_error_code();
			case Code.FailedPrecondition:
				return m.account_error_no_change();
			case Code.ResourceExhausted:
				return m.account_error_too_many();
			default:
				return m.error_unavailable();
		}
	}

	async function run(action: () => Promise<string>) {
		busy = true;
		failure = '';
		notice = '';
		try {
			notice = await action();
			await sessionState.refresh();
			await load();
		} catch (error) {
			failure = message(error);
		} finally {
			busy = false;
		}
	}

	async function rename(event: SubmitEvent) {
		event.preventDefault();
		await run(async () => {
			await withStepUp(() => accountApi.account.setDisplayName({ displayName: name.trim() }));
			return m.account_name_saved();
		});
	}

	async function requestChange(event: SubmitEvent) {
		event.preventDefault();
		const to = email.trim();
		await run(async () => {
			const res = await withStepUp(() => accountApi.account.requestEmailChange({ email: to }));
			email = '';
			return res.codeSent ? m.account_code_sent({ email: to }) : m.account_email_saved();
		});
	}

	async function confirmChange(event: SubmitEvent) {
		event.preventDefault();
		await run(async () => {
			await accountApi.account.confirmEmailChange({ code: code.trim() });
			code = '';
			return m.account_email_saved();
		});
	}

	async function cancelChange() {
		await run(async () => {
			await accountApi.account.cancelEmailChange({});
			return '';
		});
	}

	async function exportData() {
		await run(async () => {
			const res = await withStepUp(() => accountApi.account.exportAccountData({}));
			saveFile(res.json, 'sermon-account.json', 'application/json');
			return m.account_exported();
		});
	}
</script>

{#if status === 'loading'}
	<p role="status" class="text-muted-foreground">{m.loading()}</p>
{:else if status === 'error' || !account}
	<p role="alert" class="text-sm text-destructive">{m.error_unavailable()}</p>
{:else}
	<div class="flex flex-col gap-6">
		{#if failure}
			<p
				role="alert"
				class="max-w-2xl rounded-md border border-destructive/50 p-3 text-sm text-destructive"
			>
				{failure}
			</p>
		{/if}
		{#if notice}
			<p role="status" class="max-w-2xl rounded-md border bg-muted p-3 text-sm">{notice}</p>
		{/if}
		<Card.Root class="max-w-2xl">
			<Card.Header>
				<Card.Title>
					<span class="flex items-center gap-2"
						><UserRound class="size-5" aria-hidden="true" />{m.account_profile()}</span
					>
				</Card.Title>
				<Card.Description>
					{m.account_since({
						date: account.createTime
							? formatTimestamp(timestampDate(account.createTime).getTime() / 1000, locale)
							: '–'
					})}
				</Card.Description>
			</Card.Header>
			<Card.Content class="flex flex-col gap-6">
				<form class="flex flex-wrap items-end gap-3" onsubmit={rename}>
					<div class="flex flex-col gap-1">
						<Label for="account-name">{m.account_name()}</Label>
						<Input id="account-name" class="w-72" maxlength={200} bind:value={name} required />
					</div>
					<Button
						type="submit"
						disabled={busy || !name.trim() || name.trim() === account.displayName}
						>{m.account_name_save()}</Button
					>
				</form>
				<div class="flex flex-col gap-3">
					<p class="text-sm">
						{m.account_email_current()} <span class="font-medium break-all">{account.email}</span>
					</p>
					{#if account.pendingEmail}
						<form class="flex flex-col gap-3 rounded-md border p-3" onsubmit={confirmChange}>
							<p class="text-sm">
								{m.account_pending({
									email: account.pendingEmail,
									time: account.pendingExpireTime
										? formatTimestamp(
												timestampDate(account.pendingExpireTime).getTime() / 1000,
												locale
											)
										: '–'
								})}
							</p>
							<div class="flex flex-wrap items-end gap-3">
								<div class="flex flex-col gap-1">
									<Label for="account-code">{m.account_code()}</Label>
									<Input
										id="account-code"
										class="w-40 font-mono"
										autocomplete="one-time-code"
										maxlength={16}
										bind:value={code}
										required
									/>
								</div>
								<Button type="submit" disabled={busy || code.trim().length < 8}
									>{m.account_code_confirm()}</Button
								>
								<Button type="button" variant="ghost" disabled={busy} onclick={cancelChange}
									>{m.account_code_cancel()}</Button
								>
							</div>
						</form>
					{/if}
					<form class="flex flex-wrap items-end gap-3" onsubmit={requestChange}>
						<div class="flex flex-col gap-1">
							<Label for="account-email">{m.account_email_new()}</Label>
							<Input
								id="account-email"
								type="email"
								class="w-72"
								maxlength={320}
								autocomplete="email"
								bind:value={email}
								required
							/>
						</div>
						<Button type="submit" variant="outline" disabled={busy || !email.trim()}
							>{m.account_email_change()}</Button
						>
					</form>
					<p class="text-sm text-muted-foreground">
						{account.emailVerification
							? m.account_email_hint_code()
							: m.account_email_hint_direct()}
					</p>
				</div>
			</Card.Content>
		</Card.Root>
		<Card.Root class="max-w-2xl">
			<Card.Header>
				<Card.Title>{m.account_export_title()}</Card.Title>
				<Card.Description>{m.account_export_text()}</Card.Description>
			</Card.Header>
			<Card.Content>
				<Button variant="outline" disabled={busy} onclick={exportData}>
					<Download aria-hidden="true" />
					{m.account_export()}
				</Button>
			</Card.Content>
		</Card.Root>
	</div>
{/if}
