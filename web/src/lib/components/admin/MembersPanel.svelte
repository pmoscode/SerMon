<script lang="ts">
	import X from '@lucide/svelte/icons/x';
	import type { Member, NodeGroup } from '#lib/api/gen/sermon/web/v1/access_pb.js';
	import { Role } from '#lib/api/gen/sermon/web/v1/access_pb.js';
	import { api } from '#lib/api/client.js';
	import { adminError, roleName } from '#lib/admin/admin.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { m } from '#lib/paraglide/messages.js';
	import { sessionState } from '#lib/stores/session.svelte.js';
	import { withStepUp } from '#lib/stores/stepup.svelte.js';
	import RoleForm from './RoleForm.svelte';

	/** Members with their roles: grant, revoke, remove (all with step-up). */
	let {
		members,
		groups,
		onChange
	}: { members: Member[]; groups: NodeGroup[]; onChange: () => Promise<void> } = $props();

	const groupName = (id: string) =>
		id ? (groups.find((g) => g.id === id)?.name ?? '–') : m.admin_all_groups();
	let granting = $state<string | undefined>();
	let role = $state(Role.VIEWER);
	let groupId = $state('');
	let confirmRemove = $state<string | undefined>();
	let failure = $state('');
	let busy = $state(false);

	async function run(action: () => Promise<unknown>) {
		busy = true;
		failure = '';
		try {
			await withStepUp(action);
			await onChange();
			granting = undefined;
			confirmRemove = undefined;
		} catch (error) {
			failure = adminError(error);
		} finally {
			busy = false;
		}
	}

	function grant(event: SubmitEvent, userId: string) {
		event.preventDefault();
		void run(() => api.access.grantRole({ userId, groupId, role }));
	}
</script>

<div class="flex flex-col gap-4">
	{#if failure}
		<p role="alert" class="rounded-md border border-destructive/50 p-3 text-sm text-destructive">
			{failure}
		</p>
	{/if}
	<ul class="divide-y rounded-lg border">
		{#each members as member (member.userId)}
			<li class="flex flex-col gap-2 px-3 py-3">
				<div class="flex flex-wrap items-start justify-between gap-2">
					<div>
						<p class="font-medium">
							{member.displayName}
							{#if member.userId === sessionState.current?.userId}<span
									class="text-xs text-muted-foreground">({m.admin_you()})</span
								>{/if}
						</p>
						<p class="text-sm text-muted-foreground">{member.email}</p>
					</div>
					<span class="flex gap-1">
						<Button
							size="sm"
							variant="ghost"
							onclick={() => (granting = granting === member.userId ? undefined : member.userId)}
						>
							{m.admin_grant()}
						</Button>
						{#if confirmRemove === member.userId}
							<Button
								size="sm"
								variant="destructive"
								disabled={busy}
								onclick={() => run(() => api.access.removeMember({ userId: member.userId }))}
							>
								{m.admin_remove_confirm()}
							</Button>
							<Button size="sm" variant="ghost" onclick={() => (confirmRemove = undefined)}
								>{m.stepup_cancel()}</Button
							>
						{:else}
							<Button size="sm" variant="ghost" onclick={() => (confirmRemove = member.userId)}
								>{m.admin_remove()}</Button
							>
						{/if}
					</span>
				</div>
				<ul
					class="flex flex-wrap gap-2"
					aria-label={m.admin_roles_of({ name: member.displayName })}
				>
					{#each member.bindings as binding (binding.id)}
						<li class="inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs">
							{roleName(binding.role)} · {groupName(binding.groupId)}
							<button
								type="button"
								class="rounded-full p-0.5 hover:bg-muted"
								aria-label={m.admin_revoke({
									role: roleName(binding.role),
									scope: groupName(binding.groupId)
								})}
								disabled={busy}
								onclick={() => run(() => api.access.revokeRole({ bindingId: binding.id }))}
							>
								<X class="size-3" aria-hidden="true" />
							</button>
						</li>
					{:else}
						<li class="text-xs text-muted-foreground">{m.admin_no_roles()}</li>
					{/each}
				</ul>
				{#if granting === member.userId}
					<form class="flex flex-wrap items-end gap-2" onsubmit={(e) => grant(e, member.userId)}>
						<RoleForm id="grant-{member.userId}" {groups} bind:role bind:groupId />
						<Button type="submit" size="sm" disabled={busy}>{m.admin_grant_submit()}</Button>
					</form>
				{/if}
			</li>
		{/each}
	</ul>
</div>
