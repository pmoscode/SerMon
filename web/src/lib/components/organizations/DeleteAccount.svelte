<script lang="ts">
	import UserX from '@lucide/svelte/icons/user-x';
	import { Code, codeOf } from '#lib/api/client.js';
	import { accountApi } from '#lib/api/account.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { m } from '#lib/paraglide/messages.js';
	import { needsStepUp, withStepUp } from '#lib/stores/stepup.svelte.js';

	/**
	 * Deletes an account on request of its owner (V1-17), for the superadmin: the address twice,
	 * then a step-up; the server refuses superadmins and the last admin of an organization.
	 */
	let email = $state('');
	let repeat = $state('');
	let busy = $state(false);
	let failure = $state('');
	let notice = $state('');
	const same = $derived(
		email.trim() !== '' && email.trim().toLowerCase() === repeat.trim().toLowerCase()
	);

	function message(error: unknown): string {
		if (needsStepUp(error)) return '';
		switch (codeOf(error)) {
			case Code.NotFound:
				return m.orgs_account_unknown();
			case Code.FailedPrecondition:
				return m.orgs_account_refused();
			case Code.InvalidArgument:
				return m.account_error_invalid();
			case Code.PermissionDenied:
				return m.admin_error_forbidden();
			default:
				return m.error_unavailable();
		}
	}

	async function remove(event: SubmitEvent) {
		event.preventDefault();
		if (!same) return;
		busy = true;
		failure = '';
		notice = '';
		const address = email.trim();
		try {
			const res = await withStepUp(() => accountApi.admin.deleteAccount({ email: address }));
			notice = m.orgs_account_deleted({ email: address, count: res.memberships });
			email = '';
			repeat = '';
		} catch (error) {
			failure = message(error);
		} finally {
			busy = false;
		}
	}
</script>

<section
	class="flex max-w-2xl flex-col gap-3 rounded-lg border p-4"
	aria-labelledby="delete-account"
>
	<h2 id="delete-account" class="flex items-center gap-2 text-lg font-medium">
		<UserX class="size-5" aria-hidden="true" />{m.orgs_account_title()}
	</h2>
	<p class="text-sm text-muted-foreground">{m.orgs_account_text()}</p>
	{#if failure}
		<p role="alert" class="rounded-md border border-destructive/50 p-3 text-sm text-destructive">
			{failure}
		</p>
	{/if}
	{#if notice}
		<p role="status" class="rounded-md border bg-muted p-3 text-sm">{notice}</p>
	{/if}
	<form class="flex flex-wrap items-end gap-3" onsubmit={remove}>
		<div class="flex flex-col gap-1">
			<Label for="delete-account-email">{m.orgs_account_email()}</Label>
			<Input
				id="delete-account-email"
				type="email"
				class="w-64"
				maxlength={320}
				bind:value={email}
				required
			/>
		</div>
		<div class="flex flex-col gap-1">
			<Label for="delete-account-repeat">{m.orgs_account_repeat()}</Label>
			<Input
				id="delete-account-repeat"
				type="email"
				class="w-64"
				maxlength={320}
				bind:value={repeat}
				required
			/>
		</div>
		<Button type="submit" variant="destructive" disabled={busy || !same}
			>{m.orgs_account_delete()}</Button
		>
	</form>
</section>
