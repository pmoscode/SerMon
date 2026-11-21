<script lang="ts">
	import type { MessageInitShape } from '@bufbuild/protobuf';
	import { api } from '#lib/api/client.js';
	import { alertingApi } from '#lib/api/alerting.js';
	import type { NodeGroup } from '#lib/api/gen/sermon/web/v1/access_pb.js';
	import type { NotificationChannel } from '#lib/api/gen/sermon/web/v1/notification_pb.js';
	import {
		PolicyGrouping,
		type NotificationPolicy,
		type PolicySpecSchema
	} from '#lib/api/gen/sermon/web/v1/notification_policy_pb.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { durationText } from '#lib/notify/forms.js';
	import { notifyError } from '#lib/notify/notify.js';
	import { m } from '#lib/paraglide/messages.js';
	import { withStepUp } from '#lib/stores/stepup.svelte.js';
	import PolicyForm from './PolicyForm.svelte';

	/** Notification policies (M2-14): which alerts go to which channels, repeat and escalation. */
	let policies = $state<NotificationPolicy[]>([]);
	let channels = $state<NotificationChannel[]>([]);
	let groups = $state<NodeGroup[]>([]);
	let status = $state<'loading' | 'ready' | 'error'>('loading');
	let failure = $state('');
	let busy = $state(false);
	let editing = $state<NotificationPolicy | 'new' | undefined>(undefined);
	let confirmDelete = $state('');

	async function load() {
		try {
			const [p, c, g] = await Promise.all([
				alertingApi.policies.listPolicies({}),
				alertingApi.channels.listChannels({}),
				api.access.listNodeGroups({})
			]);
			policies = p.policies;
			channels = c.channels;
			groups = g.groups;
			status = 'ready';
		} catch {
			status = 'error';
		}
	}

	$effect(() => {
		void load();
	});

	async function run(action: () => Promise<unknown>): Promise<boolean> {
		busy = true;
		failure = '';
		try {
			await withStepUp(action);
			await load();
			return true;
		} catch (error) {
			failure = notifyError(error);
			return false;
		} finally {
			busy = false;
		}
	}

	async function save(spec: MessageInitShape<typeof PolicySpecSchema>) {
		const current = editing;
		const ok = await run(() =>
			current === 'new' || !current
				? alertingApi.policies.createPolicy({ spec })
				: alertingApi.policies.updatePolicy({ policyId: current.id, spec })
		);
		if (ok) editing = undefined;
	}

	async function remove(p: NotificationPolicy) {
		if (confirmDelete !== p.id) {
			confirmDelete = p.id;
			return;
		}
		confirmDelete = '';
		await run(() => alertingApi.policies.deletePolicy({ policyId: p.id }));
	}

	const names = (ids: string[]) =>
		ids.map((id) => channels.find((c) => c.id === id)?.name ?? '–').join(', ');
</script>

<div class="flex flex-col gap-4">
	{#if status === 'loading'}
		<p role="status" class="text-muted-foreground">{m.loading()}</p>
	{:else if status === 'error'}
		<div class="flex items-center gap-3">
			<p role="alert" class="text-destructive">{m.error_unavailable()}</p>
			<Button variant="outline" onclick={load}>{m.retry()}</Button>
		</div>
	{:else}
		{#if failure}
			<p role="alert" class="rounded-md border border-destructive/50 p-3 text-sm text-destructive">
				{failure}
			</p>
		{/if}
		{#if policies.length === 0}
			<p class="rounded-md border p-3 text-sm" role="note">{m.policies_fallback()}</p>
		{/if}
		{#if editing}
			<PolicyForm
				policy={editing === 'new' ? undefined : editing}
				{channels}
				{groups}
				{busy}
				onSubmit={save}
				onCancel={() => (editing = undefined)}
			/>
		{:else}
			<Button class="self-start" onclick={() => (editing = 'new')}>{m.policy_new()}</Button>
		{/if}
		<ul class="divide-y rounded-lg border" aria-label={m.policies_title()}>
			{#each policies as p (p.id)}
				{@const s = p.spec}
				<li class="flex flex-wrap items-center justify-between gap-2 px-3 py-3 text-sm">
					<span>
						<span class="font-medium">{s?.name}</span>
						{#if !s?.enabled}<span class="ml-1 rounded-md border px-1.5 py-0.5 text-xs"
								>{m.channel_disabled()}</span
							>{/if}
						<span class="block text-xs text-muted-foreground">
							{m.policy_summary({ channels: names(s?.channelIds ?? []) })}
							· {s?.grouping === PolicyGrouping.DIGEST
								? m.policy_grouping_digest()
								: m.policy_grouping_node()}
							{#if s?.repeatMinutes}· {m.policy_repeat_every({
									time: durationText(s.repeatMinutes)
								})}{/if}
							{#if s?.escalation}· {m.policy_escalates({
									time: durationText(s.escalation.afterMinutes),
									channels: names(s.escalation.channelIds)
								})}{/if}
						</span>
					</span>
					<span class="flex gap-1">
						<Button size="sm" variant="ghost" onclick={() => (editing = p)}
							>{m.channel_edit()}</Button
						>
						<Button size="sm" variant="ghost" disabled={busy} onclick={() => remove(p)}>
							{confirmDelete === p.id ? m.admin_delete_confirm() : m.admin_delete()}
						</Button>
					</span>
				</li>
			{/each}
		</ul>
	{/if}
</div>
