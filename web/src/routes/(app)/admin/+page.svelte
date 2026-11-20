<script lang="ts">
	import type { Invitation, Member, NodeGroup } from '#lib/api/gen/sermon/web/v1/access_pb.js';
	import { api } from '#lib/api/client.js';
	import { adminError } from '#lib/admin/admin.js';
	import AuditPanel from '#lib/components/admin/AuditPanel.svelte';
	import GroupsPanel from '#lib/components/admin/GroupsPanel.svelte';
	import InvitationsPanel from '#lib/components/admin/InvitationsPanel.svelte';
	import MembersPanel from '#lib/components/admin/MembersPanel.svelte';
	import SecurityPolicyPanel from '#lib/components/admin/SecurityPolicyPanel.svelte';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import * as Tabs from '#lib/components/ui/tabs/index.js';
	import { m } from '#lib/paraglide/messages.js';
	import { sessionState } from '#lib/stores/session.svelte.js';

	let groups = $state<NodeGroup[]>([]);
	let members = $state<Member[]>([]);
	let invitations = $state<Invitation[]>([]);
	let status = $state<'loading' | 'ready'>('loading');
	let failure = $state('');

	async function load() {
		failure = '';
		try {
			const [g, mem, inv] = await Promise.all([
				api.access.listNodeGroups({}),
				api.access.listMembers({}),
				api.access.listInvitations({})
			]);
			groups = g.groups;
			members = mem.members;
			invitations = inv.invitations;
		} catch (error) {
			failure = adminError(error);
		} finally {
			status = 'ready';
		}
	}

	$effect(() => {
		if (sessionState.current?.orgAdmin) void load();
	});
</script>

<svelte:head>
	<title>{m.admin_title()} · SerMon</title>
</svelte:head>

<div class="mb-6 flex items-center gap-2">
	<h1 class="text-2xl font-semibold tracking-tight">{m.admin_title()}</h1>
	<HelpTip topic="pages/admin" />
</div>

{#if !sessionState.current?.orgAdmin}
	<p role="alert" class="text-sm">{m.admin_error_forbidden()}</p>
{:else if status === 'loading'}
	<p role="status" class="text-muted-foreground">{m.loading()}</p>
{:else}
	{#if failure}
		<p
			role="alert"
			class="mb-4 rounded-md border border-destructive/50 p-3 text-sm text-destructive"
		>
			{failure}
		</p>
	{/if}
	<Tabs.Root value="members">
		<Tabs.List aria-label={m.admin_title()}>
			<Tabs.Trigger value="members">{m.admin_tab_members()}</Tabs.Trigger>
			<Tabs.Trigger value="invitations">{m.admin_tab_invitations()}</Tabs.Trigger>
			<Tabs.Trigger value="groups">{m.admin_tab_groups()}</Tabs.Trigger>
			<Tabs.Trigger value="security">{m.admin_tab_security()}</Tabs.Trigger>
			<Tabs.Trigger value="audit">{m.admin_tab_audit()}</Tabs.Trigger>
		</Tabs.List>
		<Tabs.Content value="members" class="pt-4"
			><MembersPanel {members} {groups} onChange={load} /></Tabs.Content
		>
		<Tabs.Content value="invitations" class="pt-4">
			<InvitationsPanel {invitations} {groups} onChange={load} />
		</Tabs.Content>
		<Tabs.Content value="groups" class="pt-4"><GroupsPanel {groups} onChange={load} /></Tabs.Content
		>
		<Tabs.Content value="security" class="pt-4"><SecurityPolicyPanel /></Tabs.Content>
		<Tabs.Content value="audit" class="pt-4"><AuditPanel {members} /></Tabs.Content>
	</Tabs.Root>
{/if}
