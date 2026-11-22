<script lang="ts">
	import type { NodeGroup } from '#lib/api/gen/sermon/web/v1/access_pb.js';
	import { adminApi } from '#lib/api/admin.js';
	import { adminError } from '#lib/admin/admin.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { m } from '#lib/paraglide/messages.js';
	import { withStepUp } from '#lib/stores/stepup.svelte.js';

	/** Node groups: create, rename, delete (deleting needs a step-up). */
	let { groups, onChange }: { groups: NodeGroup[]; onChange: () => Promise<void> } = $props();

	let name = $state('');
	let editing = $state<string | undefined>();
	let editName = $state('');
	let confirmDelete = $state<string | undefined>();
	let failure = $state('');
	let busy = $state(false);

	async function run(action: () => Promise<unknown>) {
		busy = true;
		failure = '';
		try {
			await withStepUp(action);
			await onChange();
		} catch (error) {
			failure = adminError(error);
		} finally {
			busy = false;
		}
	}

	async function create(event: SubmitEvent) {
		event.preventDefault();
		if (!name.trim()) return;
		await run(() => adminApi.access.createNodeGroup({ name: name.trim() }));
		if (!failure) name = '';
	}

	async function rename(event: SubmitEvent, id: string) {
		event.preventDefault();
		await run(() => adminApi.access.renameNodeGroup({ groupId: id, name: editName.trim() }));
		if (!failure) editing = undefined;
	}
</script>

<div class="flex flex-col gap-4">
	{#if failure}
		<p role="alert" class="rounded-md border border-destructive/50 p-3 text-sm text-destructive">
			{failure}
		</p>
	{/if}
	<form class="flex flex-wrap items-end gap-2" onsubmit={create}>
		<div class="flex flex-col gap-1">
			<Label for="group-name">{m.admin_group_new()}</Label>
			<Input id="group-name" class="w-64" bind:value={name} maxlength={100} />
		</div>
		<Button type="submit" disabled={busy || !name.trim()}>{m.admin_create()}</Button>
	</form>
	<ul class="divide-y rounded-lg border">
		{#each groups as group (group.id)}
			<li class="flex flex-wrap items-center justify-between gap-2 px-3 py-2">
				{#if editing === group.id}
					<form class="flex flex-wrap items-center gap-2" onsubmit={(e) => rename(e, group.id)}>
						<Label for="rename-{group.id}" class="sr-only">{m.admin_group_name()}</Label>
						<Input id="rename-{group.id}" class="w-56" bind:value={editName} maxlength={100} />
						<Button type="submit" size="sm" disabled={busy || !editName.trim()}
							>{m.admin_save()}</Button
						>
						<Button type="button" size="sm" variant="ghost" onclick={() => (editing = undefined)}
							>{m.stepup_cancel()}</Button
						>
					</form>
				{:else}
					<span class="font-medium">{group.name}</span>
					<span class="flex gap-1">
						<Button
							size="sm"
							variant="ghost"
							onclick={() => {
								editing = group.id;
								editName = group.name;
							}}>{m.admin_rename()}</Button
						>
						{#if confirmDelete === group.id}
							<Button
								size="sm"
								variant="destructive"
								disabled={busy}
								onclick={() => run(() => adminApi.access.deleteNodeGroup({ groupId: group.id }))}
							>
								{m.admin_delete_confirm()}
							</Button>
							<Button size="sm" variant="ghost" onclick={() => (confirmDelete = undefined)}
								>{m.stepup_cancel()}</Button
							>
						{:else}
							<Button size="sm" variant="ghost" onclick={() => (confirmDelete = group.id)}
								>{m.admin_delete()}</Button
							>
						{/if}
					</span>
				{/if}
			</li>
		{:else}
			<li class="px-3 py-4 text-sm text-muted-foreground">{m.admin_groups_none()}</li>
		{/each}
	</ul>
</div>
