<script lang="ts">
	import type { NodeGroup } from '#lib/api/gen/sermon/web/v1/access_pb.js';
	import { Role } from '#lib/api/gen/sermon/web/v1/access_pb.js';
	import { roleName, roles } from '#lib/admin/admin.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { m } from '#lib/paraglide/messages.js';

	/** Role and scope (one group or all groups) for grants and invitations. */
	let {
		id,
		groups,
		role = $bindable(Role.VIEWER),
		groupId = $bindable('')
	}: { id: string; groups: NodeGroup[]; role?: Role; groupId?: string } = $props();

	const selectClass =
		'h-9 rounded-md border border-input bg-background px-2 text-sm focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none';
</script>

<div class="flex flex-col gap-1">
	<Label for="{id}-role">{m.admin_role()}</Label>
	<select id="{id}-role" class={selectClass} bind:value={role}>
		{#each roles as r (r)}
			<option value={r}>{roleName(r)}</option>
		{/each}
	</select>
</div>
<div class="flex flex-col gap-1">
	<Label for="{id}-group">{m.admin_scope()}</Label>
	<select id="{id}-group" class={selectClass} bind:value={groupId}>
		<option value="">{m.admin_all_groups()}</option>
		{#each groups as g (g.id)}
			<option value={g.id}>{g.name}</option>
		{/each}
	</select>
</div>
