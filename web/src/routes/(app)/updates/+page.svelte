<script lang="ts">
	import ShieldAlert from '@lucide/svelte/icons/shield-alert';
	import { timestampDate } from '@bufbuild/protobuf/wkt';
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import {
		type ListFleetUpdatesResponse,
		type NodeUpdates,
		type Rollout
	} from '#lib/api/gen/sermon/web/v1/update_pb.js';
	import { UpdateStatus } from '#lib/api/gen/sermon/web/v1/update_status_pb.js';
	import { AgentUpdate, type UpdatePolicy } from '#lib/api/gen/sermon/web/v1/update_policy_pb.js';
	import { updatesApi } from '#lib/api/updates.js';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import PolicyEditor from '#lib/components/updates/PolicyEditor.svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import { formatAgo } from '#lib/format/format.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { displayText } from '#lib/logs/text.js';
	import { m } from '#lib/paraglide/messages.js';
	import { sessionState } from '#lib/stores/session.svelte.js';
	import {
		channelLabel,
		modeLabel,
		rolloutLabel,
		rolloutNodeLabel,
		statusLabel
	} from '#lib/updates/labels.js';

	/**
	 * The updates of the fleet (KONZEPT §16, M4-15): the nodes by status, the age of their oldest
	 * open security update, the latest rollouts and the next windows of the groups' policies.
	 */
	type Filter = UpdateStatus | 'reboot' | '';

	let fleet = $state<ListFleetUpdatesResponse>();
	let rollouts = $state<Rollout[]>([]);
	let policies = $state<UpdatePolicy[]>([]);
	let releases = $state('');
	let status = $state<'loading' | 'ready' | 'error'>('loading');
	let filter = $state<Filter>('');
	let editing = $state(page.url.searchParams.get('group') ?? '');
	let now = $state(new Date());

	const locale = $derived(localeState.current);
	const dateFormat = $derived(
		new Intl.DateTimeFormat(locale, {
			weekday: 'short',
			day: 'numeric',
			month: 'short',
			hour: '2-digit',
			minute: '2-digit'
		})
	);

	async function load() {
		try {
			const [f, r, p, a] = await Promise.all([
				updatesApi.updates.listFleetUpdates({}),
				updatesApi.updates.listRollouts({ pageSize: 20 }),
				updatesApi.policies.listUpdatePolicies({}),
				updatesApi.updates.listAgentReleases({})
			]);
			fleet = f;
			rollouts = r.rollouts;
			policies = p.policies;
			releases = [
				...new Set(a.releases.map((x) => `${channelLabel(x.channel)} ${x.version}`))
			].join(' · ');
			now = new Date();
			status = 'ready';
		} catch {
			status = 'error';
		}
	}

	$effect(() => {
		void load();
	});

	const nodes = $derived(fleet?.nodes ?? []);
	const shown = $derived(
		nodes.filter((n) =>
			filter === '' ? true : filter === 'reboot' ? n.rebootRequired : n.status === filter
		)
	);
	const filters = $derived<{ value: Filter; label: string; count: number }[]>([
		{ value: '', label: m.updatespage_all(), count: nodes.length },
		{
			value: UpdateStatus.SECURITY,
			label: statusLabel(UpdateStatus.SECURITY),
			count: fleet?.withSecurity ?? 0
		},
		{
			value: UpdateStatus.UPDATES,
			label: statusLabel(UpdateStatus.UPDATES),
			count: fleet?.withUpdates ?? 0
		},
		{ value: 'reboot', label: m.updatespage_reboot(), count: fleet?.rebootRequired ?? 0 },
		{
			value: UpdateStatus.CURRENT,
			label: statusLabel(UpdateStatus.CURRENT),
			count: fleet?.current ?? 0
		},
		{
			value: UpdateStatus.UNAVAILABLE,
			label: statusLabel(UpdateStatus.UNAVAILABLE),
			count: fleet?.unavailable ?? 0
		},
		{
			value: UpdateStatus.UNKNOWN,
			label: statusLabel(UpdateStatus.UNKNOWN),
			count: fleet?.unknown ?? 0
		}
	]);

	const ago = (t: { seconds: bigint; nanos: number } | undefined) =>
		t ? formatAgo(timestampDate(t as never), now, locale) : '–';
	const nodeHref = (n: NodeUpdates) =>
		`${resolve('/(app)/nodes/[id]', { id: n.nodeId })}?tab=updates`;
</script>

<svelte:head>
	<title>{m.updatespage_title()} · SerMon</title>
</svelte:head>

<div class="mb-6 flex items-center gap-2">
	<h1 class="text-2xl font-semibold tracking-tight">{m.updatespage_title()}</h1>
	<HelpTip topic="pages/updates" />
</div>

{#if status === 'loading'}
	<p role="status" class="text-muted-foreground">{m.loading()}</p>
{:else if status === 'error'}
	<div class="flex items-center gap-3">
		<p role="alert" class="text-destructive">{m.error_unavailable()}</p>
		<Button variant="outline" onclick={load}>{m.retry()}</Button>
	</div>
{:else}
	<div class="flex flex-col gap-8">
		<section aria-labelledby="updates-nodes" class="flex flex-col gap-3">
			<h2 id="updates-nodes" class="text-lg font-medium">{m.updatespage_nodes()}</h2>
			<div class="flex flex-wrap gap-2" role="group" aria-label={m.updatespage_summary()}>
				{#each filters as f (f.value)}
					<button
						type="button"
						class="rounded-lg border px-3 py-1.5 text-left aria-pressed:border-primary aria-pressed:ring-2 aria-pressed:ring-primary/30"
						aria-pressed={filter === f.value}
						onclick={() => (filter = filter === f.value ? '' : f.value)}
					>
						<span class="text-lg font-semibold tabular-nums">{f.count}</span>
						<span class="text-xs text-muted-foreground">{f.label}</span>
					</button>
				{/each}
			</div>
			{#if shown.length === 0}
				<p class="text-sm text-muted-foreground">{m.updatespage_none()}</p>
			{:else}
				<div class="overflow-x-auto rounded-md border">
					<table class="w-full text-sm">
						<caption class="sr-only">{m.updatespage_nodes()}</caption>
						<thead class="bg-muted text-left">
							<tr>
								<th scope="col" class="px-2 py-1">{m.updatespage_col_node()}</th>
								<th scope="col" class="px-2 py-1">{m.updatespage_col_state()}</th>
								<th scope="col" class="px-2 py-1 text-right">{m.updatespage_col_security()}</th>
								<th scope="col" class="px-2 py-1 text-right">{m.updatespage_col_total()}</th>
								<th scope="col" class="px-2 py-1">{m.updatespage_col_oldest()}</th>
								<th scope="col" class="px-2 py-1">{m.updatespage_col_reboot()}</th>
								<th scope="col" class="px-2 py-1">{m.updatespage_col_report()}</th>
							</tr>
						</thead>
						<tbody>
							{#each shown as n (n.nodeId)}
								<tr class="border-t">
									<th scope="row" class="px-2 py-1 text-left font-medium">
										<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- resolved, with the tab -->
										<a class="underline-offset-2 hover:underline" href={nodeHref(n)}>{n.hostname}</a
										>
									</th>
									<td class="px-2 py-1">
										{#if n.status === UpdateStatus.SECURITY}
											<span class="inline-flex items-center gap-1 text-red-700 dark:text-red-400">
												<ShieldAlert class="size-4" aria-hidden="true" />
												{statusLabel(n.status)}
											</span>
										{:else}
											{statusLabel(n.status)}
										{/if}
										{#if n.reason}<span class="block text-xs text-muted-foreground"
												>{displayText(n.reason)}</span
											>{/if}
									</td>
									<td class="px-2 py-1 text-right tabular-nums">{n.securityCount}</td>
									<td class="px-2 py-1 text-right tabular-nums">{n.totalCount}</td>
									<td class="px-2 py-1"
										>{n.oldestSecuritySince ? ago(n.oldestSecuritySince) : '–'}</td
									>
									<td class="px-2 py-1">{n.rebootRequired ? m.updates_reboot_required() : '–'}</td>
									<td class="px-2 py-1">{ago(n.collectedAt)}</td>
								</tr>
							{/each}
						</tbody>
					</table>
				</div>
			{/if}
		</section>

		<section aria-labelledby="updates-rollouts" class="flex flex-col gap-3">
			<h2 id="updates-rollouts" class="text-lg font-medium">{m.updatespage_rollouts()}</h2>
			{#if rollouts.length === 0}
				<p class="text-sm text-muted-foreground">{m.updatespage_rollouts_none()}</p>
			{:else}
				<ul class="divide-y rounded-md border text-sm">
					{#each rollouts as r (r.rolloutId)}
						<li class="flex flex-col gap-1 px-3 py-2">
							<div class="flex flex-wrap gap-x-3">
								<span class="font-medium">{r.groupName}</span>
								<span>{r.manual ? m.updatespage_manual() : m.updatespage_scheduled()}</span>
								<span>{rolloutLabel(r.state)}</span>
								{#if r.windowStart}<span class="text-muted-foreground"
										>{dateFormat.format(timestampDate(r.windowStart))}</span
									>{/if}
							</div>
							{#if r.reason}<p class="text-red-700 dark:text-red-400">
									{displayText(r.reason)}
								</p>{/if}
							{#if r.nodes.length > 0}
								<p class="text-muted-foreground">
									{#each r.nodes as n, i (n.nodeId)}{i > 0 ? ' · ' : ''}{displayText(n.hostname)}: {rolloutNodeLabel(
											n.state
										)}{n.reason ? ` (${displayText(n.reason)})` : ''}{/each}
								</p>
							{/if}
						</li>
					{/each}
				</ul>
			{/if}
		</section>

		<section aria-labelledby="updates-policies" class="flex flex-col gap-3">
			<h2 id="updates-policies" class="flex items-center gap-1 text-lg font-medium">
				{m.updatespage_policies()}
				<HelpTip topic="concepts/update-policy" />
			</h2>
			{#if releases}
				<p class="text-sm text-muted-foreground">{m.updatespage_releases({ list: releases })}</p>
			{/if}
			<ul class="divide-y rounded-md border text-sm">
				{#each policies as p (p.groupId)}
					<li class="flex flex-col gap-2 px-3 py-2">
						<div class="flex flex-wrap items-center gap-x-3">
							<span class="font-medium">{p.groupName}</span>
							<span>{modeLabel(p.spec?.mode ?? 0)}</span>
							{#if p.spec?.agentUpdate === AgentUpdate.AUTO}
								<span>{m.updatespage_agent_auto({ channel: channelLabel(p.spec.channel) })}</span>
							{/if}
							<span class="text-muted-foreground">
								{m.updatespage_next_window()}:
								{p.nextWindow
									? dateFormat.format(timestampDate(p.nextWindow))
									: m.updatespage_no_window()}
							</span>
							{#if sessionState.current?.orgAdmin}
								<Button
									variant="ghost"
									size="sm"
									class="ml-auto"
									onclick={() => (editing = editing === p.groupId ? '' : p.groupId)}
									>{m.updatespage_edit()}</Button
								>
							{/if}
						</div>
						{#if editing === p.groupId && sessionState.current?.orgAdmin}
							<PolicyEditor policy={p} onsaved={load} />
						{/if}
					</li>
				{/each}
			</ul>
		</section>
	</div>
{/if}
