<script lang="ts">
	import type { ProcessInfo } from '#lib/api/gen/sermon/web/v1/live_view_pb.js';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import * as Sheet from '#lib/components/ui/sheet/index.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { displayText } from '#lib/logs/text.js';
	import { m } from '#lib/paraglide/messages.js';
	import {
		cpuText,
		memoryText,
		processName,
		rssText,
		runtimeText,
		stateText,
		userText
	} from '#lib/processes/display.js';

	/**
	 * The details of a process: the whole redacted command line, unit and container. Fields the
	 * agent could not read show "n/a" with an explanation. Read only (D19).
	 */
	let {
		process,
		parent,
		ended,
		onClose
	}: {
		process: ProcessInfo | undefined;
		/** The parent, if it is in the snapshot. */
		parent: ProcessInfo | undefined;
		/** The process is gone from the newest snapshot. */
		ended: boolean;
		onClose: () => void;
	} = $props();

	const locale = $derived(localeState.current);
	const p = $derived(process);
</script>

{#snippet na()}
	<span class="inline-flex items-center gap-0.5 text-muted-foreground"
		>{m.procs_na()}<HelpTip topic="concepts/process-na" /></span
	>
{/snippet}

{#snippet field(label: string, value: string | undefined)}
	<div class="flex flex-col gap-0.5">
		<dt class="text-xs text-muted-foreground">{label}</dt>
		<dd class="break-words">
			{#if value === undefined}{@render na()}{:else}{value}{/if}
		</dd>
	</div>
{/snippet}

<Sheet.Root
	open={process !== undefined}
	onOpenChange={(open) => {
		if (!open) onClose();
	}}
>
	<Sheet.Content
		side="right"
		class="overflow-y-auto sm:max-w-lg"
		closeLabel={m.procs_drawer_close()}
	>
		{#if p}
			<Sheet.Header>
				<Sheet.Title class="pr-8 break-all">
					{m.procs_drawer_title({ name: processName(p), pid: String(p.pid) })}
				</Sheet.Title>
				<Sheet.Description>{m.procs_drawer_text()}</Sheet.Description>
			</Sheet.Header>
			<div class="flex flex-col gap-4 px-4 pb-6">
				{#if ended}
					<p role="status" class="rounded-md border border-border p-2 text-sm">
						{m.procs_drawer_ended()}
					</p>
				{/if}
				{#if p.kernelThread}
					<p class="text-sm text-muted-foreground">{m.procs_kernel_thread()}</p>
				{/if}
				<dl class="grid grid-cols-2 gap-3 text-sm">
					{@render field(
						m.procs_field_parent(),
						parent
							? m.procs_drawer_title({ name: processName(parent), pid: String(parent.pid) })
							: String(p.ppid)
					)}
					{@render field(
						m.procs_field_user(),
						userText(p) === undefined
							? undefined
							: p.user && p.uid !== undefined
								? `${userText(p)} (${m.procs_field_uid({ uid: String(p.uid) })})`
								: userText(p)
					)}
					{@render field(m.procs_field_state(), stateText(p))}
					{@render field(
						m.procs_field_cpu(),
						p.cpuRatio === undefined ? m.procs_cpu_first() : cpuText(p, locale)
					)}
					{@render field(
						m.procs_field_memory(),
						rssText(p, locale) === undefined
							? undefined
							: `${rssText(p, locale)} · ${memoryText(p, locale) ?? m.procs_na()}`
					)}
					{@render field(m.procs_field_threads(), String(p.threads))}
					{@render field(m.procs_field_runtime(), runtimeText(p, locale))}
					{@render field(m.procs_field_unit(), p.unit ? displayText(p.unit) : '–')}
					{@render field(m.procs_field_container(), p.container ? displayText(p.container) : '–')}
				</dl>
				<section class="flex flex-col gap-1" aria-labelledby="proc-cmdline">
					<h3 id="proc-cmdline" class="text-xs text-muted-foreground">{m.procs_field_cmdline()}</h3>
					{#if p.cmdlineHidden}
						<p class="text-sm">{m.procs_cmdline_hidden()}</p>
					{:else if p.cmdline === undefined}
						<p class="text-sm">{@render na()}</p>
					{:else if p.cmdline === ''}
						<p class="text-sm text-muted-foreground">{m.procs_cmdline_none()}</p>
					{:else}
						<pre
							class="rounded-md border border-border bg-muted p-2 font-mono text-xs break-all whitespace-pre-wrap">{displayText(
								p.cmdline
							)}</pre>
						<p class="text-xs text-muted-foreground">
							{m.procs_cmdline_redacted()}
							{#if p.cmdlineTruncated}{m.procs_cmdline_truncated()}{/if}
						</p>
					{/if}
				</section>
			</div>
		{/if}
	</Sheet.Content>
</Sheet.Root>
