<script lang="ts">
	import type { LogLine } from '#lib/api/gen/sermon/web/v1/log_pb.js';
	import LogText from '#lib/components/logs/LogText.svelte';
	import SeverityBadge from '#lib/components/logs/SeverityBadge.svelte';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { lineMs } from '#lib/logs/lines.js';
	import { displayText } from '#lib/logs/text.js';
	import { m } from '#lib/paraglide/messages.js';

	/** The newest log lines, newest first; host data stays text. */
	const MAX_LINES = 50;
	let {
		title,
		lines,
		more = false
	}: { title: string; lines: LogLine[]; more?: boolean } = $props();

	const time = $derived(
		new Intl.DateTimeFormat(localeState.current, {
			day: '2-digit',
			month: '2-digit',
			hour: '2-digit',
			minute: '2-digit',
			second: '2-digit'
		})
	);
	const shown = $derived(lines.slice(0, MAX_LINES));
	const stamp = (l: LogLine) => {
		const ms = lineMs(l);
		return Number.isFinite(ms) && ms > 0 ? time.format(ms) : '–';
	};
</script>

{#if shown.length === 0}
	<p class="text-sm text-muted-foreground">{m.widget_no_lines()}</p>
{:else}
	<ol class="flex flex-col gap-0.5 font-mono text-xs" aria-label={title}>
		{#each shown as l, i (i)}
			<li class="flex min-w-0 items-baseline gap-2">
				<span class="shrink-0 text-muted-foreground tabular-nums">{stamp(l)}</span>
				<span class="max-w-28 shrink-0 truncate">{displayText(l.hostname)}</span>
				<SeverityBadge severity={l.severity} />
				<span class="min-w-0 flex-1"><LogText text={l.message} /></span>
			</li>
		{/each}
	</ol>
	{#if more || lines.length > MAX_LINES}
		<p class="text-xs text-muted-foreground">{m.widget_more_lines()}</p>
	{/if}
{/if}
