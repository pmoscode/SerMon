<script lang="ts">
	import type { NodeGroup } from '#lib/api/gen/sermon/web/v1/access_pb.js';
	import { Role } from '#lib/api/gen/sermon/web/v1/access_pb.js';
	import type { ListOidcMappingsResponse } from '#lib/api/gen/sermon/web/v1/oidc_mapping_pb.js';
	import { adminApi } from '#lib/api/admin.js';
	import { adminError, roleName } from '#lib/admin/admin.js';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { m } from '#lib/paraglide/messages.js';
	import { withStepUp } from '#lib/stores/stepup.svelte.js';
	import RoleForm from './RoleForm.svelte';

	/**
	 * Groups of the OIDC provider mapped to roles (M3-21, M3-22): every sign-in gives and takes
	 * the mapped roles; creating and deleting need a step-up.
	 */
	let { groups }: { groups: NodeGroup[] } = $props();

	let data = $state<ListOidcMappingsResponse | undefined>();
	let status = $state<'loading' | 'ready'>('loading');
	let oidcGroup = $state('');
	let role = $state(Role.VIEWER);
	let groupId = $state('');
	let confirmDelete = $state<string | undefined>();
	let failure = $state('');
	let busy = $state(false);

	const groupName = (id: string) =>
		id ? (groups.find((g) => g.id === id)?.name ?? '–') : m.admin_all_groups();

	async function load() {
		try {
			data = await adminApi.oidcMappings.listOidcMappings({});
		} catch (error) {
			failure = adminError(error);
		} finally {
			status = 'ready';
		}
	}

	$effect(() => {
		void load();
	});

	async function run(action: () => Promise<unknown>) {
		busy = true;
		failure = '';
		try {
			await withStepUp(action);
			confirmDelete = undefined;
			await load();
			return true;
		} catch (error) {
			failure = adminError(error);
			return false;
		} finally {
			busy = false;
		}
	}

	async function create(event: SubmitEvent) {
		event.preventDefault();
		const name = oidcGroup.trim();
		if (!name) return;
		if (
			await run(() => adminApi.oidcMappings.createOidcMapping({ oidcGroup: name, groupId, role }))
		)
			oidcGroup = '';
	}
</script>

<div class="flex flex-col gap-4">
	<div class="flex items-center gap-1">
		<h2 class="text-lg font-medium">{m.oidcmap_title()}</h2>
		<HelpTip topic="concepts/oidc-group-mappings" />
	</div>
	{#if status === 'loading'}
		<p role="status" class="text-muted-foreground">{m.loading()}</p>
	{:else}
		{#if data && !data.oidc}
			<p class="rounded-md border border-dashed p-3 text-sm">{m.oidcmap_no_provider()}</p>
		{:else if data}
			<p class="text-sm text-muted-foreground">
				{data.provisioning ? m.oidcmap_provisioning_on() : m.oidcmap_provisioning_off()}
			</p>
		{/if}
		{#if failure}
			<p role="alert" class="rounded-md border border-destructive/50 p-3 text-sm text-destructive">
				{failure}
			</p>
		{/if}
		<form class="flex flex-wrap items-end gap-2" onsubmit={create}>
			<div class="flex flex-col gap-1">
				<Label for="oidcmap-name">{m.oidcmap_group()}</Label>
				<Input
					id="oidcmap-name"
					class="w-64"
					bind:value={oidcGroup}
					maxlength={255}
					autocomplete="off"
					aria-describedby="oidcmap-name-hint"
				/>
			</div>
			<RoleForm id="oidcmap" {groups} bind:role bind:groupId />
			<Button type="submit" disabled={busy || !oidcGroup.trim()}>{m.admin_create()}</Button>
		</form>
		<p id="oidcmap-name-hint" class="-mt-2 text-xs text-muted-foreground">
			{m.oidcmap_group_hint()}
		</p>
		<div class="overflow-x-auto rounded-lg border">
			<table class="w-full text-sm">
				<thead class="bg-muted text-left text-xs">
					<tr>
						<th scope="col" class="px-3 py-2 font-medium">{m.oidcmap_group()}</th>
						<th scope="col" class="px-3 py-2 font-medium">{m.admin_role()}</th>
						<th scope="col" class="px-3 py-2 font-medium">{m.admin_scope()}</th>
						<th scope="col" class="px-3 py-2"><span class="sr-only">{m.oidcmap_actions()}</span></th
						>
					</tr>
				</thead>
				<tbody>
					{#each data?.mappings ?? [] as mapping (mapping.id)}
						<tr class="border-t">
							<td class="px-3 py-2 font-mono break-all">{mapping.oidcGroup}</td>
							<td class="px-3 py-2">{roleName(mapping.role)}</td>
							<td class="px-3 py-2">{groupName(mapping.groupId)}</td>
							<td class="px-3 py-2 text-right whitespace-nowrap">
								{#if confirmDelete === mapping.id}
									<Button
										size="sm"
										variant="destructive"
										disabled={busy}
										onclick={() =>
											run(() => adminApi.oidcMappings.deleteOidcMapping({ id: mapping.id }))}
										>{m.admin_delete_confirm()}</Button
									>
									<Button size="sm" variant="ghost" onclick={() => (confirmDelete = undefined)}
										>{m.stepup_cancel()}</Button
									>
								{:else}
									<Button
										size="sm"
										variant="ghost"
										aria-label={m.oidcmap_delete({ group: mapping.oidcGroup })}
										onclick={() => (confirmDelete = mapping.id)}>{m.admin_delete()}</Button
									>
								{/if}
							</td>
						</tr>
					{:else}
						<tr>
							<td colspan="4" class="px-3 py-4 text-sm text-muted-foreground">{m.oidcmap_none()}</td
							>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/if}
</div>
