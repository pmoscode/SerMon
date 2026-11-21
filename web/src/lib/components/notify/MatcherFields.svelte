<script lang="ts">
	import { AlertSeverity } from '#lib/api/gen/sermon/web/v1/notification_policy_pb.js';
	import type { NodeGroup } from '#lib/api/gen/sermon/web/v1/access_pb.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { m } from '#lib/paraglide/messages.js';

	/** The matchers shared by policies and silences: severities, groups, rule patterns, labels. */
	let {
		id,
		groups,
		severities = $bindable(),
		groupIds = $bindable(),
		rules = $bindable(),
		labels = $bindable(),
		severity = true
	}: {
		id: string;
		groups: { id: string; name: string }[] | NodeGroup[];
		severities: AlertSeverity[];
		groupIds: string[];
		rules: string;
		labels: string;
		severity?: boolean;
	} = $props();

	function toggle<T>(list: T[], value: T, on: boolean): T[] {
		return on ? [...new Set([...list, value])] : list.filter((v) => v !== value);
	}
</script>

<fieldset class="flex flex-col gap-3 rounded-md border p-3">
	<legend class="px-1 text-sm font-medium">{m.matchers_title()}</legend>
	{#if severity}
		<div class="flex flex-wrap gap-3 text-sm" role="group" aria-label={m.matchers_severities()}>
			<span class="text-muted-foreground">{m.matchers_severities()}:</span>
			{#each [{ s: AlertSeverity.WARNING, l: m.alert_state_warning() }, { s: AlertSeverity.CRITICAL, l: m.alert_state_critical() }] as o (o.s)}
				<label class="flex items-center gap-1">
					<input
						type="checkbox"
						class="size-4"
						checked={severities.includes(o.s)}
						onchange={(e) => (severities = toggle(severities, o.s, e.currentTarget.checked))}
					/>
					{o.l}
				</label>
			{/each}
		</div>
	{/if}
	{#if groups.length > 0}
		<div class="flex flex-wrap gap-3 text-sm" role="group" aria-label={m.matchers_groups()}>
			<span class="text-muted-foreground">{m.matchers_groups()}:</span>
			{#each groups as g (g.id)}
				<label class="flex items-center gap-1">
					<input
						type="checkbox"
						class="size-4"
						checked={groupIds.includes(g.id)}
						onchange={(e) => (groupIds = toggle(groupIds, g.id, e.currentTarget.checked))}
					/>
					{g.name}
				</label>
			{/each}
		</div>
	{/if}
	<div class="grid gap-3 sm:grid-cols-2">
		<div class="flex flex-col gap-1">
			<Label for={`${id}-rules`}>{m.matchers_rules()}</Label>
			<input
				id={`${id}-rules`}
				class="h-9 rounded-md border border-input bg-background px-2 font-mono text-sm focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
				placeholder="security.*, system.cpu.*"
				bind:value={rules}
			/>
		</div>
		<div class="flex flex-col gap-1">
			<Label for={`${id}-labels`}>{m.matchers_labels()}</Label>
			<textarea
				id={`${id}-labels`}
				rows="2"
				class="rounded-md border border-input bg-background px-2 py-1 font-mono text-sm focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
				placeholder="category=security"
				bind:value={labels}></textarea>
		</div>
	</div>
	<p class="text-xs text-muted-foreground">{m.matchers_hint()}</p>
</fieldset>
