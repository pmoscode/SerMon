<script lang="ts">
	import { loadHistory } from '#lib/alerts/alerts.svelte.js';
	import { alertValue, fieldLabel, fieldValue, stateName } from '#lib/alerts/view.js';
	import {
		AlertHistoryKind,
		type AlertHistoryEntry,
		type AlertKey
	} from '#lib/api/gen/sermon/web/v1/alert_pb.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { m } from '#lib/paraglide/messages.js';
	import { timestampDate } from '@bufbuild/protobuf/wkt';

	/** The history of an instance, loaded when shown. Host data is shown as text. */
	let { alertKey, unit, label }: { alertKey: AlertKey; unit: string; label: string } = $props();

	let entries = $state<AlertHistoryEntry[]>([]);
	let status = $state<'loading' | 'ready' | 'error'>('loading');

	async function load() {
		status = 'loading';
		try {
			entries = await loadHistory(alertKey);
			status = 'ready';
		} catch {
			status = 'error';
		}
	}

	$effect(() => {
		void load();
	});

	const timeFormat = $derived(
		new Intl.DateTimeFormat(localeState.current, { dateStyle: 'short', timeStyle: 'medium' })
	);

	function describe(e: AlertHistoryEntry): string {
		switch (e.kind) {
			case AlertHistoryKind.ACKNOWLEDGED:
				return m.alert_history_acknowledged({ name: e.by || '–' });
			case AlertHistoryKind.FLAPPING_STARTED:
				return m.alert_history_flapping_started();
			case AlertHistoryKind.FLAPPING_STOPPED:
				return m.alert_history_flapping_stopped();
			default:
				return m.alert_history_changed({ from: stateName(e.from), to: stateName(e.to) });
		}
	}
</script>

<section aria-label={label} class="mt-3 border-t pt-3">
	{#if status === 'loading'}
		<p role="status" class="text-sm text-muted-foreground">{m.loading()}</p>
	{:else if status === 'error'}
		<div class="flex items-center gap-3">
			<p role="alert" class="text-sm text-destructive">{m.error_unavailable()}</p>
			<Button variant="outline" size="sm" onclick={load}>{m.retry()}</Button>
		</div>
	{:else if entries.length === 0}
		<p class="text-sm text-muted-foreground">{m.alert_history_empty()}</p>
	{:else}
		<ol class="flex flex-col gap-2 text-sm">
			{#each entries as e, i (i)}
				<li class="flex flex-wrap gap-x-3 gap-y-1">
					<time
						class="text-muted-foreground tabular-nums"
						datetime={e.time ? timestampDate(e.time).toISOString() : undefined}
					>
						{e.time ? timeFormat.format(timestampDate(e.time)) : '–'}
					</time>
					<span class="font-medium">{describe(e)}</span>
					{#if e.kind === AlertHistoryKind.STATE_CHANGED}
						<span class="tabular-nums">{alertValue(unit, e.value, localeState.current)}</span>
					{/if}
					{#each Object.entries(e.fields) as [name, value] (name)}
						<span
							><span class="text-muted-foreground">{fieldLabel(name)}:</span>
							{fieldValue(name, value)}</span
						>
					{/each}
				</li>
			{/each}
		</ol>
	{/if}
</section>
