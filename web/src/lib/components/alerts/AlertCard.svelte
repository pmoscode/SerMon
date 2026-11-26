<script lang="ts">
	import BellOff from '@lucide/svelte/icons/bell-off';
	import ChartLine from '@lucide/svelte/icons/chart-line';
	import CheckCheck from '@lucide/svelte/icons/check-check';
	import Repeat from '@lucide/svelte/icons/repeat';
	import ScrollText from '@lucide/svelte/icons/scroll-text';
	import ShieldAlert from '@lucide/svelte/icons/shield-alert';
	import { timestampDate } from '@bufbuild/protobuf/wkt';
	import { resolve } from '$app/paths';
	import { chartsQuery } from '#lib/alerts/links.js';
	import {
		alertValue,
		dimensionText,
		fieldLabel,
		ruleName,
		unverifiedSource
	} from '#lib/alerts/view.js';
	import type { Alert } from '#lib/api/gen/sermon/web/v1/alert_pb.js';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import { formatAgo } from '#lib/format/format.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { alertLogsQuery } from '#lib/logs/link.js';
	import { m } from '#lib/paraglide/messages.js';
	import AlertStateBadge from './AlertStateBadge.svelte';
	import AlertTimeline from './AlertTimeline.svelte';

	/**
	 * One alert with its state, values and actions. Host names, dimensions and fields come from
	 * the monitored hosts: they are shown as text only (no {@html}).
	 */
	let {
		alert,
		now,
		onAcknowledge,
		onSilence
	}: {
		alert: Alert;
		now: Date;
		onAcknowledge: (alert: Alert) => Promise<string>;
		/** Set when the caller may silence the alert's group: opens a prefilled silence. */
		onSilence?: (alert: Alert) => void;
	} = $props();

	let open = $state(false);
	let busy = $state(false);
	let error = $state('');

	const name = $derived(ruleName(alert, localeState.current));
	const dimension = $derived(dimensionText(alert));
	const since = $derived(alert.since ? timestampDate(alert.since) : undefined);
	/** When it began to fire; pending alerts by their start. */
	const fired = $derived(alert.fireTime ? timestampDate(alert.fireTime) : since);
	const unverified = $derived(unverifiedSource(alert.fields));
	// The source is the note below, not another field.
	const fields = $derived(Object.entries(alert.fields).filter(([name]) => name !== 'source'));
	const historyId = $derived(
		`history-${alert.key?.ruleId}-${alert.key?.nodeId}-${alert.key?.dimension}`.replace(
			/[^A-Za-z0-9_-]/g,
			'_'
		)
	);

	async function acknowledge() {
		busy = true;
		error = await onAcknowledge(alert);
		busy = false;
	}
</script>

<article
	class="rounded-lg border bg-card p-4 text-card-foreground"
	aria-label={`${name} – ${alert.hostname}`}
>
	<div class="flex flex-wrap items-start gap-x-3 gap-y-2">
		<AlertStateBadge state={alert.state} class="mt-0.5" />
		<div class="flex min-w-0 flex-1 flex-col gap-0.5">
			<h2 class="font-medium break-words">{name}</h2>
			<p class="text-sm text-muted-foreground">
				{m.alert_on()}
				<a
					class="text-foreground underline-offset-4 hover:underline"
					href={resolve('/(app)/nodes/[id]', { id: alert.key?.nodeId ?? '' })}>{alert.hostname}</a
				>
				· {alert.groupName}
				{#if dimension}
					· <span class="break-all">{dimension}</span>
				{/if}
			</p>
		</div>
		<div class="flex flex-wrap items-center gap-2">
			{#if alert.muted}
				<span
					class="inline-flex items-center gap-1 rounded-md border px-1.5 py-0.5 text-xs"
					title={m.alert_muted_hint()}
				>
					<BellOff class="size-3.5" aria-hidden="true" />
					{m.alert_muted()}
					<span class="sr-only">– {m.alert_muted_hint()}</span>
				</span>
			{/if}
			{#if alert.flapping}
				<span
					class="inline-flex items-center gap-1 rounded-md border px-1.5 py-0.5 text-xs"
					title={m.alert_flapping_hint()}
				>
					<Repeat class="size-3.5" aria-hidden="true" />
					{m.alert_flapping()}
					<span class="sr-only">– {m.alert_flapping_hint()}</span>
				</span>
			{/if}
		</div>
	</div>

	<dl
		class="mt-3 grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm sm:grid-cols-[auto_1fr_auto_1fr]"
	>
		<dt class="text-muted-foreground">{m.alert_value()}</dt>
		<dd class="tabular-nums">{alertValue(alert.unit, alert.value, localeState.current)}</dd>
		{#if since}
			<dt class="text-muted-foreground">{m.alert_since()}</dt>
			<dd>
				<time datetime={since.toISOString()}>{formatAgo(since, now, localeState.current)}</time>
			</dd>
		{/if}
		{#each fields as [field, value] (field)}
			<dt class="text-muted-foreground">{fieldLabel(field)}</dt>
			<dd class="break-all">{value}</dd>
		{/each}
	</dl>

	{#if unverified}
		<p
			role="note"
			class="mt-2 inline-flex items-center gap-1 text-sm text-amber-700 dark:text-amber-400"
		>
			<ShieldAlert class="size-4 shrink-0" aria-hidden="true" />
			{m.alert_source_unverified_note()}
			<HelpTip topic="concepts/unverified-source" />
		</p>
	{/if}

	{#if alert.acknowledgedBy}
		<p class="mt-2 inline-flex items-center gap-1 text-sm text-muted-foreground">
			<CheckCheck class="size-4" aria-hidden="true" />
			{m.alert_acknowledged({
				name: alert.acknowledgedBy,
				ago: alert.acknowledgeTime
					? formatAgo(timestampDate(alert.acknowledgeTime), now, localeState.current)
					: ''
			})}
		</p>
	{/if}

	<div class="mt-3 flex flex-wrap items-center gap-2">
		{#if alert.canAcknowledge}
			<Button
				size="sm"
				disabled={busy}
				onclick={acknowledge}
				aria-label={m.alert_acknowledge_label({ rule: name, host: alert.hostname })}
			>
				{m.alert_acknowledge()}
			</Button>
		{/if}
		{#if onSilence}
			<Button
				size="sm"
				variant="outline"
				onclick={() => onSilence(alert)}
				aria-label={m.alert_silence_label({ rule: name, host: alert.hostname })}
			>
				{m.alert_silence()}
			</Button>
		{/if}
		<Button
			size="sm"
			variant="outline"
			aria-expanded={open}
			aria-controls={historyId}
			aria-label={m.alert_history_label({ rule: name, host: alert.hostname })}
			onclick={() => (open = !open)}
		>
			{m.alert_history()}
		</Button>
		<Button
			size="sm"
			variant="ghost"
			href={resolve('/(app)/nodes/[id]', { id: alert.key?.nodeId ?? '' })}
		>
			{m.alert_open_node()}
		</Button>
		{#if fired && alert.key?.nodeId}
			<Button
				size="sm"
				variant="ghost"
				href={`${resolve('/(app)/nodes/[id]', { id: alert.key.nodeId })}${chartsQuery(fired.getTime(), now.getTime(), alert.labels.category)}`}
			>
				<ChartLine aria-hidden="true" />
				{m.alert_charts_link()}
			</Button>
		{/if}
		{#if since && alert.key?.nodeId}
			<Button
				size="sm"
				variant="ghost"
				href={`${resolve('/(app)/logs')}${alertLogsQuery(alert.key.nodeId, since.getTime(), now.getTime(), alert.dimensionLabels.unit)}`}
			>
				<ScrollText aria-hidden="true" />
				{m.alert_logs_link()}
			</Button>
		{/if}
		{#if error}
			<p role="alert" class="text-sm text-destructive">{error}</p>
		{/if}
	</div>

	<div id={historyId}>
		{#if open && alert.key}
			<AlertTimeline
				alertKey={alert.key}
				unit={alert.unit}
				label={m.alert_history_label({ rule: name, host: alert.hostname })}
			/>
		{/if}
	</div>
</article>
