<script lang="ts">
	import { create } from '@bufbuild/protobuf';
	import Plus from '@lucide/svelte/icons/plus';
	import X from '@lucide/svelte/icons/x';
	import {
		DashboardVariableSchema,
		VariableKind,
		type DashboardVariable
	} from '#lib/api/gen/sermon/web/v1/dashboard_pb.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { MAX_VARIABLES } from '#lib/dashboards/model.js';
	import type { FleetNode } from '#lib/dashboards/variables.js';
	import { displayText } from '#lib/logs/text.js';
	import { m } from '#lib/paraglide/messages.js';

	/** The variables of a dashboard in the editor: name, node or group, and the default. */
	let {
		variables,
		fleet,
		onchange
	}: {
		variables: DashboardVariable[];
		fleet: FleetNode[];
		onchange: (v: DashboardVariable[]) => void;
	} = $props();

	const NAME = /^[a-z][a-z0-9_]{0,31}$/;
	const groups = $derived(
		[...new Map(fleet.map((n) => [n.groupId, n.groupName])).entries()].sort((a, b) =>
			a[1].localeCompare(b[1])
		)
	);
	const nodes = $derived([...fleet].sort((a, b) => a.hostname.localeCompare(b.hostname)));
	const selectClass =
		'h-9 rounded-md border border-input bg-background px-2 text-sm focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none';

	function set(i: number, change: Partial<DashboardVariable>) {
		onchange(variables.map((v, j) => (j === i ? { ...v, ...change } : v)));
	}

	function add() {
		const used = new Set(variables.map((v) => v.name));
		let name = 'node';
		for (let n = 2; used.has(name); n++) name = `node${n}`;
		onchange([...variables, create(DashboardVariableSchema, { name, kind: VariableKind.NODE })]);
	}
</script>

<fieldset class="flex flex-col gap-2 rounded-md border p-3">
	<legend class="px-1 text-sm font-medium">{m.dashboard_variables()}</legend>
	{#if variables.length === 0}
		<p class="text-sm text-muted-foreground">{m.dashboard_variables_none()}</p>
	{/if}
	{#each variables as v, i (i)}
		{@const bad = !NAME.test(v.name) || variables.some((o, j) => j !== i && o.name === v.name)}
		<div class="flex flex-wrap items-end gap-2">
			<div class="flex flex-col gap-1">
				<Label for="variable-name-{i}">{m.dashboard_variable_name()}</Label>
				<Input
					id="variable-name-{i}"
					class="w-40 font-mono"
					maxlength={32}
					value={v.name}
					aria-invalid={bad || undefined}
					aria-describedby={bad ? `variable-name-${i}-error` : undefined}
					oninput={(e) => set(i, { name: e.currentTarget.value })}
				/>
			</div>
			<div class="flex flex-col gap-1">
				<Label for="variable-kind-{i}">{m.dashboard_variable_kind()}</Label>
				<select
					id="variable-kind-{i}"
					class={selectClass}
					value={v.kind}
					onchange={(e) =>
						set(i, { kind: Number(e.currentTarget.value) as VariableKind, defaultId: '' })}
				>
					<option value={VariableKind.NODE}>{m.dashboard_variable_node()}</option>
					<option value={VariableKind.GROUP}>{m.dashboard_variable_group()}</option>
				</select>
			</div>
			<div class="flex flex-col gap-1">
				<Label for="variable-default-{i}">{m.dashboard_variable_default()}</Label>
				<select
					id="variable-default-{i}"
					class={selectClass}
					value={v.defaultId}
					onchange={(e) => set(i, { defaultId: e.currentTarget.value })}
				>
					<option value="">{m.dashboard_variable_first()}</option>
					{#if v.kind === VariableKind.GROUP}
						{#each groups as [id, name] (id)}<option value={id}>{displayText(name)}</option>{/each}
					{:else}
						{#each nodes as n (n.id)}<option value={n.id}>{displayText(n.hostname)}</option>{/each}
					{/if}
				</select>
			</div>
			<Button
				variant="ghost"
				size="icon-sm"
				aria-label={m.dashboard_variable_remove({ name: v.name })}
				onclick={() => onchange(variables.filter((_, j) => j !== i))}
			>
				<X aria-hidden="true" />
			</Button>
			{#if bad}
				<p id="variable-name-{i}-error" class="w-full text-xs text-destructive">
					{m.dashboard_variable_bad_name()}
				</p>
			{/if}
		</div>
	{/each}
	<div>
		<Button variant="outline" size="sm" disabled={variables.length >= MAX_VARIABLES} onclick={add}>
			<Plus aria-hidden="true" />
			{m.dashboard_variable_add()}
		</Button>
	</div>
</fieldset>
