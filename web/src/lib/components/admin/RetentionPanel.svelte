<script lang="ts">
	import type { GetRetentionResponse } from '#lib/api/gen/sermon/web/v1/access_pb.js';
	import { adminApi } from '#lib/api/admin.js';
	import { adminError } from '#lib/admin/admin.js';
	import { checkRetention, parseDays, type RetentionLimits } from '#lib/admin/retention.js';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { m } from '#lib/paraglide/messages.js';
	import { withStepUp } from '#lib/stores/stepup.svelte.js';

	/**
	 * How long the organization keeps its logs and security events (M5-30): within the instance's
	 * limits and the superadmin's cap; empty is the instance's default (for events
	 * SERMON_EVENT_RETENTION, sent along). Saving needs a step-up.
	 */
	const id = $props.id();
	let current = $state<GetRetentionResponse | undefined>();
	// A number input binds a number, or null when empty.
	let logs = $state<string | number | null>('');
	let events = $state<string | number | null>('');
	let failure = $state('');
	let done = $state('');
	let busy = $state(false);

	$effect(() => {
		adminApi.access
			.getRetention({})
			.then((res) => {
				current = res;
				logs = res.logRetentionDays ? String(res.logRetentionDays) : '';
				events = res.eventRetentionDays ? String(res.eventRetentionDays) : '';
			})
			.catch((error: unknown) => (failure = adminError(error)));
	});

	const limits = $derived<RetentionLimits | undefined>(
		current && {
			logMin: current.logMinDays,
			logMax: current.logMaxDays,
			eventMin: current.eventMinDays,
			eventMax: current.eventMaxDays,
			cap: current.capDays
		}
	);
	const logDays = $derived(parseDays(String(logs ?? '')));
	const eventDays = $derived(parseDays(String(events ?? '')));
	const invalid = $derived(limits ? checkRetention(logDays, eventDays, limits) : []);

	async function save() {
		busy = true;
		failure = '';
		done = '';
		try {
			await withStepUp(() =>
				adminApi.access.setRetention({
					logRetentionDays: logDays ?? 0,
					eventRetentionDays: eventDays ?? 0
				})
			);
			done = m.retention_saved();
		} catch (error) {
			failure = adminError(error);
		} finally {
			busy = false;
		}
	}
</script>

<div class="flex max-w-xl flex-col gap-4">
	<p class="flex items-center gap-1 text-sm text-muted-foreground">
		{m.retention_intro()}
		<HelpTip topic="concepts/retention" />
	</p>
	{#if !current && !failure}
		<p role="status" class="text-muted-foreground">{m.loading()}</p>
	{:else if current && limits}
		<form
			class="flex flex-col gap-4"
			onsubmit={(e) => {
				e.preventDefault();
				void save();
			}}
		>
			<div class="flex flex-col gap-1">
				<label for="{id}-logs" class="text-sm font-medium">{m.retention_logs()}</label>
				<Input
					id="{id}-logs"
					type="number"
					min={limits.logMin}
					max={limits.logMax}
					bind:value={logs}
					class="max-w-32"
					aria-describedby="{id}-logs-range"
				/>
				<p id="{id}-logs-range" class="text-xs text-muted-foreground">
					{m.retention_range({ min: limits.logMin, max: limits.logMax, fallback: limits.logMax })}
				</p>
			</div>
			<div class="flex flex-col gap-1">
				<label for="{id}-events" class="text-sm font-medium">{m.retention_events()}</label>
				<Input
					id="{id}-events"
					type="number"
					min={limits.eventMin}
					max={limits.eventMax}
					bind:value={events}
					class="max-w-32"
					aria-describedby="{id}-events-range"
				/>
				<p id="{id}-events-range" class="text-xs text-muted-foreground">
					{m.retention_range({
						min: limits.eventMin,
						max: limits.eventMax,
						fallback: current.eventDefaultDays || limits.eventMax
					})}
				</p>
			</div>
			{#if limits.cap > 0}
				<p class="text-sm">{m.retention_cap({ cap: limits.cap })}</p>
			{/if}
			{#if invalid.includes('logs')}
				<p class="text-sm text-destructive">{m.retention_invalid_logs()}</p>
			{/if}
			{#if invalid.includes('events')}
				<p class="text-sm text-destructive">{m.retention_invalid_events()}</p>
			{/if}
			<Button type="submit" class="self-start" disabled={busy || invalid.length > 0}>
				{m.retention_save()}
			</Button>
		</form>
	{/if}
	{#if done}
		<p class="text-sm" aria-live="polite">{done}</p>
	{/if}
	{#if failure}
		<p role="alert" class="rounded-md border border-destructive/50 p-3 text-sm text-destructive">
			{failure}
		</p>
	{/if}
</div>
