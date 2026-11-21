<script lang="ts">
	import BellRing from '@lucide/svelte/icons/bell-ring';
	import { AlertsState } from '#lib/alerts/alerts.svelte.js';
	import { countAlerts, filterAlerts, groupsOf, type Kind } from '#lib/alerts/view.js';
	import type { Alert } from '#lib/api/gen/sermon/web/v1/alert_pb.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { coversGroups } from '#lib/notify/forms.js';
	import { m } from '#lib/paraglide/messages.js';
	import { sessionState } from '#lib/stores/session.svelte.js';
	import AlertCard from './AlertCard.svelte';

	/** The active alerts with filters, refreshed every 15 s (M2-16). */
	let { onSilence }: { onSilence?: (alert: Alert) => void } = $props();
	const alerts = new AlertsState();
	const silenceGroups = $derived(sessionState.current?.silenceGroupIds ?? []);
	let search = $state('');
	let group = $state('');
	let stateFilter = $state<Kind | ''>('');

	$effect(() => alerts.start());

	const list = $derived(alerts.alerts);
	const shown = $derived(filterAlerts(list, { search, group, state: stateFilter }));
	const counts = $derived(countAlerts(list));
	const groups = $derived(groupsOf(list));
	const now = $derived(new Date(alerts.updatedAt ?? Date.now()));
	const timeFormat = $derived(new Intl.DateTimeFormat(localeState.current, { timeStyle: 'short' }));

	function reset() {
		search = '';
		group = '';
		stateFilter = '';
	}

	const acknowledge = (a: Alert) => (a.key ? alerts.acknowledge(a.key) : Promise.resolve(''));
</script>

{#if alerts.status === 'loading'}
	<p role="status" class="text-muted-foreground">{m.loading()}</p>
{:else if alerts.status === 'error'}
	<div class="flex flex-col items-start gap-3">
		<p role="alert" class="text-destructive">{m.error_unavailable()}</p>
		<Button variant="outline" onclick={() => alerts.load()}>{m.retry()}</Button>
	</div>
{:else if list.length === 0}
	<section
		class="flex flex-col items-center gap-3 rounded-lg border border-dashed p-12 text-center"
		aria-labelledby="alerts-empty"
	>
		<BellRing class="size-10 text-muted-foreground" aria-hidden="true" />
		<h2 id="alerts-empty" class="text-lg font-medium">{m.alerts_empty_title()}</h2>
		<p class="max-w-md text-sm text-muted-foreground">{m.alerts_empty_text()}</p>
	</section>
{:else}
	<div class="flex flex-col gap-4">
		<p class="text-sm">
			{m.alerts_summary({ firing: String(counts.firing), critical: String(counts.critical) })}
		</p>
		<div class="flex flex-wrap items-end gap-3" role="search" aria-label={m.alerts_filter()}>
			<div class="flex flex-col gap-1">
				<Label for="alerts-search">{m.alerts_search()}</Label>
				<Input
					id="alerts-search"
					type="search"
					class="w-64"
					bind:value={search}
					placeholder={m.alerts_search_hint()}
				/>
			</div>
			<div class="flex flex-col gap-1">
				<Label for="alerts-state">{m.alerts_state()}</Label>
				<select
					id="alerts-state"
					class="h-9 rounded-md border border-input bg-background px-2 text-sm focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
					bind:value={stateFilter}
				>
					<option value="">{m.alerts_state_all({ count: String(list.length) })}</option>
					<option value="firing">{m.alerts_state_firing({ count: String(counts.firing) })}</option>
					<option value="pending"
						>{m.alerts_state_pending({ count: String(counts.pending) })}</option
					>
					<option value="resolved"
						>{m.alerts_state_resolved({ count: String(counts.resolved) })}</option
					>
				</select>
			</div>
			{#if groups.length > 1}
				<div class="flex flex-col gap-1">
					<Label for="alerts-group">{m.alerts_group()}</Label>
					<select
						id="alerts-group"
						class="h-9 rounded-md border border-input bg-background px-2 text-sm focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
						bind:value={group}
					>
						<option value="">{m.alerts_group_all()}</option>
						{#each groups as g (g.id)}
							<option value={g.id}>{g.name}</option>
						{/each}
					</select>
				</div>
			{/if}
			{#if alerts.stale && alerts.updatedAt}
				<p role="status" class="ml-auto text-xs text-amber-700 dark:text-amber-400">
					{m.alerts_stale({ time: timeFormat.format(alerts.updatedAt) })}
				</p>
			{/if}
		</div>

		<p class="sr-only" role="status">
			{m.alerts_shown({ shown: String(shown.length), total: String(list.length) })}
		</p>
		{#if shown.length === 0}
			<div class="flex flex-col items-start gap-3 rounded-lg border border-dashed p-6">
				<p>{m.alerts_no_match()}</p>
				<Button variant="outline" onclick={reset}>{m.alerts_reset_filter()}</Button>
			</div>
		{:else}
			<div class="flex flex-col gap-3">
				{#each shown as a (`${a.key?.ruleId}|${a.key?.nodeId}|${a.key?.dimension}`)}
					<AlertCard
						alert={a}
						{now}
						onAcknowledge={acknowledge}
						onSilence={onSilence && coversGroups(silenceGroups, [a.groupId])
							? onSilence
							: undefined}
					/>
				{/each}
			</div>
		{/if}
	</div>
{/if}
