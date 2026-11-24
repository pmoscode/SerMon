<script lang="ts">
	import { displayText } from '#lib/logs/text.js';
	import { tick } from 'svelte';
	import type { LogLine } from '#lib/api/gen/sermon/web/v1/log_pb.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Dialog from '#lib/components/ui/dialog/index.js';
	import { lineKey } from '#lib/logs/lines.js';
	import { m } from '#lib/paraglide/messages.js';
	import LogLineRow from './LogLineRow.svelte';

	/** The lines around an entry of its stream (± 50), with the entry marked. */
	let {
		line,
		load,
		onClose
	}: {
		line: LogLine | undefined;
		load: (line: LogLine) => Promise<LogLine[]>;
		onClose: () => void;
	} = $props();

	let lines = $state.raw<LogLine[]>([]);
	let status = $state<'loading' | 'ready' | 'error'>('loading');
	let list = $state<HTMLOListElement>();

	async function fetch(entry: LogLine) {
		status = 'loading';
		try {
			lines = await load(entry);
			status = 'ready';
			await tick();
			list?.querySelector('[aria-current="true"]')?.scrollIntoView({ block: 'center' });
		} catch {
			status = 'error';
		}
	}

	$effect(() => {
		if (line) void fetch(line);
	});

	const key = $derived(line ? lineKey(line) : '');
</script>

<Dialog.Root
	open={line !== undefined}
	onOpenChange={(open) => {
		if (!open) onClose();
	}}
>
	<Dialog.Content
		closeLabel={m.logs_context_close()}
		class="flex max-h-[85vh] flex-col sm:max-w-4xl"
	>
		<Dialog.Header>
			<Dialog.Title>{m.logs_context_title()}</Dialog.Title>
			<Dialog.Description>
				{m.logs_context_text({ host: line?.hostname ?? '', unit: displayText(line?.unit || '–') })}
			</Dialog.Description>
		</Dialog.Header>
		{#if status === 'loading'}
			<p role="status" class="text-sm text-muted-foreground">{m.loading()}</p>
		{:else if status === 'error'}
			<div class="flex flex-col items-start gap-2">
				<p role="alert" class="text-sm text-destructive">{m.logs_error_unavailable()}</p>
				<Button variant="outline" size="sm" onclick={() => line && fetch(line)}>{m.retry()}</Button>
			</div>
		{:else}
			<ol bind:this={list} class="min-h-0 overflow-auto rounded-md border border-border">
				{#each lines as l (lineKey(l))}
					<li><LogLineRow line={l} showHost={false} current={lineKey(l) === key} /></li>
				{/each}
			</ol>
		{/if}
	</Dialog.Content>
</Dialog.Root>
