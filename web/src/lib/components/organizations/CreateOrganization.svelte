<script lang="ts">
	import Copy from '@lucide/svelte/icons/copy';
	import { adminApi } from '#lib/api/admin.js';
	import { adminError, copyText } from '#lib/admin/admin.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { m } from '#lib/paraglide/messages.js';
	import { withStepUp } from '#lib/stores/stepup.svelte.js';

	/**
	 * A new organization (superadmin, step-up): its first admin is invited by mail, or – without
	 * mail – by a link shown once (ADR 0008). The superadmin does not become a member.
	 */
	let { onCreated }: { onCreated: () => Promise<void> } = $props();

	let name = $state('');
	let email = $state('');
	let failure = $state('');
	let busy = $state(false);
	let link = $state('');
	let sentTo = $state('');
	let copied = $state(false);

	async function create(event: SubmitEvent) {
		event.preventDefault();
		busy = true;
		failure = '';
		link = '';
		sentTo = '';
		copied = false;
		try {
			const to = email.trim();
			const res = await withStepUp(() =>
				adminApi.organization.createOrganization({ name: name.trim(), adminEmail: to })
			);
			if (res.invitationEmailed) sentTo = to;
			else link = `${location.origin}/invite#${res.invitationToken}`;
			name = '';
			email = '';
			await onCreated();
		} catch (error) {
			failure = adminError(error);
		} finally {
			busy = false;
		}
	}
</script>

<section class="flex flex-col gap-3" aria-labelledby="org-create-title">
	<h2 id="org-create-title" class="text-lg font-semibold">{m.orgs_create_title()}</h2>
	{#if failure}
		<p role="alert" class="rounded-md border border-destructive/50 p-3 text-sm text-destructive">
			{failure}
		</p>
	{/if}
	<form class="flex flex-wrap items-end gap-2" onsubmit={create}>
		<div class="flex flex-col gap-1">
			<Label for="org-new-name">{m.orgs_name()}</Label>
			<Input id="org-new-name" class="w-64" bind:value={name} maxlength={200} required />
		</div>
		<div class="flex flex-col gap-1">
			<Label for="org-new-admin">{m.orgs_admin_email()}</Label>
			<Input
				id="org-new-admin"
				type="email"
				class="w-72"
				bind:value={email}
				maxlength={320}
				autocomplete="off"
				required
			/>
		</div>
		<Button type="submit" disabled={busy || !name.trim() || !email.trim()}>{m.orgs_create()}</Button
		>
	</form>
	{#if sentTo}
		<p class="rounded-md border p-3 text-sm" role="status">
			{m.orgs_invite_sent({ email: sentTo })}
		</p>
	{/if}
	{#if link}
		<div
			class="flex flex-col gap-2 rounded-md border border-amber-500/50 p-3 text-sm"
			role="status"
		>
			<p>{m.orgs_invite_link()}</p>
			<code class="font-mono text-xs break-all select-all">{link}</code>
			<Button
				size="sm"
				variant="outline"
				class="self-start"
				onclick={async () => (copied = await copyText(link))}
			>
				<Copy aria-hidden="true" />
				{copied ? m.security_codes_copied() : m.security_codes_copy()}
			</Button>
		</div>
	{/if}
</section>
