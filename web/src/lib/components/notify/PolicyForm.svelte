<script lang="ts">
	import { untrack } from 'svelte';
	import type { NodeGroup } from '#lib/api/gen/sermon/web/v1/access_pb.js';
	import type { NotificationChannel } from '#lib/api/gen/sermon/web/v1/notification_pb.js';
	import {
		PolicyGrouping,
		type AlertSeverity,
		type NotificationPolicy,
		type PolicySpec
	} from '#lib/api/gen/sermon/web/v1/notification_policy_pb.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { labelsText, parseLabels, parsePatterns, patternsText } from '#lib/notify/forms.js';
	import { m } from '#lib/paraglide/messages.js';
	import type { MessageInitShape } from '@bufbuild/protobuf';
	import type { PolicySpecSchema } from '#lib/api/gen/sermon/web/v1/notification_policy_pb.js';
	import MatcherFields from './MatcherFields.svelte';

	let {
		policy,
		channels,
		groups,
		busy,
		onSubmit,
		onCancel
	}: {
		policy?: NotificationPolicy;
		channels: NotificationChannel[];
		groups: NodeGroup[];
		busy: boolean;
		onSubmit: (spec: MessageInitShape<typeof PolicySpecSchema>) => void;
		onCancel: () => void;
	} = $props();

	// The form starts from the policy once; later changes of the prop don't reset typing.
	const initial = untrack(() => $state.snapshot(policy?.spec)) as PolicySpec | undefined;
	let name = $state(initial?.name ?? '');
	let enabled = $state(initial?.enabled ?? true);
	let channelIds = $state<string[]>([...(initial?.channelIds ?? [])]);
	let grouping = $state(initial?.grouping ?? PolicyGrouping.NODE);
	let repeat = $state(initial?.repeatMinutes ?? 0);
	let escalate = $state(!!initial?.escalation);
	let escalationAfter = $state(initial?.escalation?.afterMinutes ?? 15);
	let escalationIds = $state<string[]>([...(initial?.escalation?.channelIds ?? [])]);
	let severities = $state<AlertSeverity[]>([...(initial?.matchers?.severities ?? [])]);
	let groupIds = $state<string[]>([...(initial?.matchers?.groupIds ?? [])]);
	let rules = $state(patternsText(initial?.matchers?.rules ?? []));
	let labels = $state(labelsText(initial?.matchers?.labels ?? {}));
	let problem = $state('');

	function toggle(list: string[], id: string, on: boolean): string[] {
		return on ? [...new Set([...list, id])] : list.filter((v) => v !== id);
	}

	function submit(event: SubmitEvent) {
		event.preventDefault();
		const parsed = parseLabels(labels);
		if ('error' in parsed) {
			problem = m.matchers_labels_invalid({ line: String(parsed.error) });
			return;
		}
		problem = '';
		onSubmit({
			name: name.trim(),
			enabled,
			channelIds,
			grouping,
			repeatMinutes: Number(repeat) || 0,
			escalation: escalate
				? { afterMinutes: Number(escalationAfter), channelIds: escalationIds }
				: undefined,
			matchers: { severities, groupIds, rules: parsePatterns(rules), labels: parsed.labels }
		});
	}

	const complete = $derived(
		name.trim() !== '' && channelIds.length > 0 && (!escalate || escalationIds.length > 0)
	);
</script>

<form
	class="flex flex-col gap-4 rounded-lg border p-4"
	onsubmit={submit}
	aria-label={initial ? m.policy_edit() : m.policy_new()}
>
	<div class="flex flex-wrap items-end gap-3">
		<div class="flex flex-col gap-1">
			<Label for="policy-name">{m.policy_name()}</Label>
			<Input id="policy-name" class="w-64" maxlength={100} bind:value={name} />
		</div>
		<div class="flex flex-col gap-1">
			<Label for="policy-grouping">{m.policy_grouping()}</Label>
			<select
				id="policy-grouping"
				class="h-9 rounded-md border border-input bg-background px-2 text-sm focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
				bind:value={grouping}
			>
				<option value={PolicyGrouping.NODE}>{m.policy_grouping_node()}</option>
				<option value={PolicyGrouping.DIGEST}>{m.policy_grouping_digest()}</option>
			</select>
		</div>
		<div class="flex flex-col gap-1">
			<Label for="policy-repeat">{m.policy_repeat()}</Label>
			<Input
				id="policy-repeat"
				type="number"
				min="0"
				max="10080"
				class="w-32"
				bind:value={repeat}
			/>
		</div>
		<label class="flex h-9 items-center gap-2 text-sm">
			<input type="checkbox" class="size-4" bind:checked={enabled} />
			{m.policy_enabled()}
		</label>
	</div>

	<fieldset class="flex flex-wrap gap-3 rounded-md border p-3 text-sm">
		<legend class="px-1 font-medium">{m.policy_channels()}</legend>
		{#each channels as c (c.id)}
			<label class="flex items-center gap-1">
				<input
					type="checkbox"
					class="size-4"
					checked={channelIds.includes(c.id)}
					onchange={(e) => (channelIds = toggle(channelIds, c.id, e.currentTarget.checked))}
				/>
				{c.name}
			</label>
		{:else}
			<p class="text-muted-foreground">{m.policy_no_channels()}</p>
		{/each}
	</fieldset>

	<MatcherFields id="policy" {groups} bind:severities bind:groupIds bind:rules bind:labels />

	<fieldset class="flex flex-col gap-3 rounded-md border p-3 text-sm">
		<legend class="px-1 font-medium">{m.policy_escalation()}</legend>
		<label class="flex items-center gap-2">
			<input type="checkbox" class="size-4" bind:checked={escalate} />
			{m.policy_escalation_on()}
		</label>
		{#if escalate}
			<div class="flex flex-col gap-1">
				<Label for="policy-escalation-after">{m.policy_escalation_after()}</Label>
				<Input
					id="policy-escalation-after"
					type="number"
					min="1"
					max="1440"
					class="w-32"
					bind:value={escalationAfter}
				/>
			</div>
			<div class="flex flex-wrap gap-3" role="group" aria-label={m.policy_escalation_channels()}>
				<span class="text-muted-foreground">{m.policy_escalation_channels()}:</span>
				{#each channels as c (c.id)}
					<label class="flex items-center gap-1">
						<input
							type="checkbox"
							class="size-4"
							checked={escalationIds.includes(c.id)}
							onchange={(e) =>
								(escalationIds = toggle(escalationIds, c.id, e.currentTarget.checked))}
						/>
						{c.name}
					</label>
				{/each}
			</div>
		{/if}
	</fieldset>

	{#if problem}<p role="alert" class="text-sm text-destructive">{problem}</p>{/if}
	<div class="flex gap-2">
		<Button type="submit" disabled={busy || !complete}
			>{initial ? m.admin_save() : m.policy_create()}</Button
		>
		<Button type="button" variant="ghost" onclick={onCancel}>{m.notify_cancel()}</Button>
	</div>
</form>
