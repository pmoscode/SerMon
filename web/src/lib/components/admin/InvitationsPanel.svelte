<script lang="ts">
	import Copy from '@lucide/svelte/icons/copy';
	import { timestampDate } from '@bufbuild/protobuf/wkt';
	import type { Invitation, NodeGroup } from '#lib/api/gen/sermon/web/v1/access_pb.js';
	import { Role } from '#lib/api/gen/sermon/web/v1/access_pb.js';
	import { api } from '#lib/api/client.js';
	import { adminError, copyText, roleName } from '#lib/admin/admin.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { formatTimestamp } from '#lib/format/format.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { m } from '#lib/paraglide/messages.js';
	import { withStepUp } from '#lib/stores/stepup.svelte.js';
	import RoleForm from './RoleForm.svelte';

	/** Invitations by link (ADR 0008): the link is shown once, nothing is mailed. */
	let {
		invitations,
		groups,
		onChange
	}: { invitations: Invitation[]; groups: NodeGroup[]; onChange: () => Promise<void> } = $props();

	let email = $state('');
	let role = $state(Role.VIEWER);
	let groupId = $state('');
	let link = $state('');
	let copied = $state(false);
	let failure = $state('');
	let busy = $state(false);
	const groupName = (id: string) =>
		id ? (groups.find((g) => g.id === id)?.name ?? '–') : m.admin_all_groups();

	async function invite(event: SubmitEvent) {
		event.preventDefault();
		busy = true;
		failure = '';
		link = '';
		copied = false;
		try {
			const res = await withStepUp(() =>
				api.access.inviteMember({ email: email.trim(), role, groupId })
			);
			link = `${location.origin}/invite#${res.token}`;
			email = '';
			await onChange();
		} catch (error) {
			failure = adminError(error);
		} finally {
			busy = false;
		}
	}

	async function revoke(id: string) {
		failure = '';
		try {
			await api.access.revokeInvitation({ invitationId: id });
			await onChange();
		} catch (error) {
			failure = adminError(error);
		}
	}
</script>

<div class="flex flex-col gap-4">
	{#if failure}
		<p role="alert" class="rounded-md border border-destructive/50 p-3 text-sm text-destructive">
			{failure}
		</p>
	{/if}
	<form class="flex flex-wrap items-end gap-2" onsubmit={invite}>
		<div class="flex flex-col gap-1">
			<Label for="invite-email">{m.field_email()}</Label>
			<Input id="invite-email" type="email" class="w-64" bind:value={email} />
		</div>
		<RoleForm id="invite" {groups} bind:role bind:groupId />
		<Button type="submit" disabled={busy || !email.includes('@')}>{m.admin_invite()}</Button>
	</form>
	{#if link}
		<div
			class="flex flex-col gap-2 rounded-md border border-amber-500/50 p-3 text-sm"
			role="status"
		>
			<p>{m.admin_invite_link()}</p>
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
	<ul class="divide-y rounded-lg border">
		{#each invitations as inv (inv.id)}
			<li class="flex flex-wrap items-center justify-between gap-2 px-3 py-2 text-sm">
				<span>
					<span class="font-medium">{inv.email}</span>
					<span class="text-muted-foreground"
						>· {roleName(inv.role)} · {groupName(inv.groupId)}</span
					>
					{#if inv.expireTime}
						<span class="block text-xs text-muted-foreground">
							{m.admin_invite_expires({
								when: formatTimestamp(
									timestampDate(inv.expireTime).getTime() / 1000,
									localeState.current
								)
							})}
						</span>
					{/if}
				</span>
				<Button size="sm" variant="ghost" onclick={() => revoke(inv.id)}
					>{m.admin_revoke_invitation()}</Button
				>
			</li>
		{:else}
			<li class="px-3 py-4 text-sm text-muted-foreground">{m.admin_invitations_none()}</li>
		{/each}
	</ul>
</div>
