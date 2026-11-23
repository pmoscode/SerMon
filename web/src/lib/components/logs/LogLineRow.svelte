<script lang="ts">
	import ChevronRight from '@lucide/svelte/icons/chevron-right';
	import ListTree from '@lucide/svelte/icons/list-tree';
	import Scissors from '@lucide/svelte/icons/scissors';
	import type { LogLine } from '#lib/api/gen/sermon/web/v1/log_pb.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { lineMs } from '#lib/logs/lines.js';
	import { sourceLabel } from '#lib/logs/labels.js';
	import { m } from '#lib/paraglide/messages.js';
	import { cn } from '#lib/utils.js';
	import LogText from './LogText.svelte';
	import SeverityBadge from './SeverityBadge.svelte';

	/** One log line. Everything but the times comes from the host and stays text. */
	let {
		line,
		showHost = true,
		current = false,
		onContext
	}: {
		line: LogLine;
		showHost?: boolean;
		/** The entry a context view is about. */
		current?: boolean;
		onContext?: (line: LogLine) => void;
	} = $props();

	let open = $state(false);

	const ms = $derived(lineMs(line));
	const time = $derived(
		new Intl.DateTimeFormat(localeState.current, {
			day: '2-digit',
			month: '2-digit',
			hour: '2-digit',
			minute: '2-digit',
			second: '2-digit',
			fractionalSecondDigits: 3
		}).format(ms)
	);
	const fields = $derived(Object.entries(line.fields).sort(([a], [b]) => a.localeCompare(b)));
	const id = $props.id();
</script>

<div
	class={cn(
		'flex flex-col gap-1 border-b border-border/60 px-2 py-1.5 font-mono text-xs',
		current && 'border-l-4 border-l-primary'
	)}
	aria-current={current ? 'true' : undefined}
>
	<div class="flex flex-wrap items-baseline gap-x-3 gap-y-1">
		<button
			type="button"
			class="inline-flex items-center gap-1 rounded-sm text-muted-foreground hover:text-foreground focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
			aria-expanded={open}
			aria-controls="{id}-fields"
			aria-label={m.logs_line_details({ time })}
			onclick={() => (open = !open)}
		>
			<ChevronRight
				class={cn('size-3.5 transition-transform', open && 'rotate-90')}
				aria-hidden="true"
			/>
			<time datetime={new Date(ms).toISOString()}>{time}</time>
		</button>
		<SeverityBadge severity={line.severity} />
		{#if showHost}<span class="font-medium">{line.hostname}</span>{/if}
		{#if line.unit}<span class="text-muted-foreground">{line.unit}</span>{/if}
		{#if current}<span class="font-sans font-semibold text-foreground">{m.logs_context_this()}</span
			>{/if}
		{#if line.truncated}
			<span class="inline-flex items-center gap-1 text-muted-foreground">
				<Scissors class="size-3" aria-hidden="true" />
				{m.logs_truncated()}
			</span>
		{/if}
	</div>
	<p class="pl-5 text-foreground"><LogText text={line.message} /></p>
	{#if open}
		<div id="{id}-fields" class="flex flex-col gap-2 pl-5">
			<dl class="grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 text-muted-foreground">
				<dt>{m.logs_field_source()}</dt>
				<dd>{sourceLabel(line.source)}</dd>
				{#each fields as [name, value] (name)}
					<dt>{name}</dt>
					<dd class="text-foreground"><LogText text={value} /></dd>
				{/each}
			</dl>
			{#if onContext}
				<Button variant="outline" size="xs" class="self-start" onclick={() => onContext(line)}>
					<ListTree aria-hidden="true" />
					{m.logs_context_open()}
				</Button>
			{/if}
		</div>
	{/if}
</div>
