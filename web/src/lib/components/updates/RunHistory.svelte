<script lang="ts">
	import CircleAlert from '@lucide/svelte/icons/circle-alert';
	import { timestampDate } from '@bufbuild/protobuf/wkt';
	import { codeOf, Code } from '#lib/api/client.js';
	import { UpdateScope, type UpdateRun } from '#lib/api/gen/sermon/web/v1/update_pb.js';
	import { updatesApi } from '#lib/api/updates.js';
	import LogText from '#lib/components/logs/LogText.svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import { formatAgo } from '#lib/format/format.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { displayText } from '#lib/logs/text.js';
	import { notifyError } from '#lib/notify/notify.js';
	import { m } from '#lib/paraglide/messages.js';
	import { runFailed, runKindLabel, runStateLabel } from '#lib/updates/labels.js';

	/**
	 * The runs of a node (M4-15), newest first; the output of a run on request, as text with
	 * control characters marked (it comes from the host's package manager, redacted by the agent).
	 */
	let { nodeId, now }: { nodeId: string; now: Date } = $props();

	const locale = $derived(localeState.current);
	let runs = $state<UpdateRun[]>([]);
	let status = $state<'loading' | 'ready' | 'error'>('loading');
	let open = $state('');
	let output = $state<UpdateRun>();
	let failure = $state('');

	// Answers count only for the node and the run still shown (M4-31): a slow one of before would
	// put another node's runs or another run's output in their place.
	async function load(node: string) {
		open = '';
		output = undefined;
		status = 'loading';
		try {
			const list = (await updatesApi.updates.listUpdateRuns({ nodeId: node, pageSize: 20 })).runs;
			if (node !== nodeId) return;
			runs = list;
			status = 'ready';
		} catch {
			if (node === nodeId) status = 'error';
		}
	}

	$effect(() => {
		void load(nodeId);
	});

	async function show(run: UpdateRun) {
		const id = run.runId;
		if (open === id) {
			open = '';
			return;
		}
		open = id;
		output = undefined;
		failure = '';
		try {
			const got = (await updatesApi.updates.getUpdateRun({ runId: id })).run;
			if (open === id) output = got;
		} catch (error) {
			if (open !== id) return;
			failure =
				codeOf(error) === Code.PermissionDenied ? m.runs_output_forbidden() : notifyError(error);
		}
	}

	function scopeText(run: UpdateRun): string {
		if (run.scope === UpdateScope.SECURITY) return ` (${m.updates_security()})`;
		return '';
	}
</script>

<section class="flex flex-col gap-2" aria-labelledby="runs-title">
	<h3 id="runs-title" class="font-medium">{m.runs_title()}</h3>
	{#if status === 'loading'}
		<p role="status" class="text-sm text-muted-foreground">{m.loading()}</p>
	{:else if status === 'error'}
		<p role="alert" class="text-sm text-destructive">{m.error_unavailable()}</p>
	{:else if runs.length === 0}
		<p class="text-sm text-muted-foreground">{m.runs_none()}</p>
	{:else}
		<ul class="divide-y rounded-md border text-sm">
			{#each runs as run (run.runId)}
				<li class="flex flex-col gap-1 px-3 py-2">
					<div class="flex flex-wrap items-center gap-x-3 gap-y-1">
						<span class="font-medium">{runKindLabel(run.kind)}{scopeText(run)}</span>
						<span
							class={runFailed(run.state)
								? 'inline-flex items-center gap-1 text-red-700 dark:text-red-400'
								: ''}
						>
							{#if runFailed(run.state)}<CircleAlert class="size-4" aria-hidden="true" />{/if}
							{runStateLabel(run.state)}
							{#if run.exitCode !== undefined}· {m.runs_exit_code({ code: run.exitCode })}{/if}
						</span>
						<span class="text-muted-foreground">
							{run.createdBy ? m.runs_by_hand() : m.runs_by_policy()}
							{#if run.createTime}· {formatAgo(timestampDate(run.createTime), now, locale)}{/if}
						</span>
						<Button variant="ghost" size="sm" class="ml-auto" onclick={() => show(run)}>
							{open === run.runId ? m.runs_output_hide() : m.runs_output()}
						</Button>
					</div>
					{#if run.reason}<p class="text-muted-foreground">{displayText(run.reason)}</p>{/if}
					{#if run.resultReason}<p>{displayText(run.resultReason)}</p>{/if}
					{#if open === run.runId}
						{#if failure}
							<p role="alert" class="text-destructive">{failure}</p>
						{:else if output}
							{#if output.outputTruncated}<p class="text-muted-foreground">
									{m.runs_output_cut()}
								</p>{/if}
							<pre
								class="max-h-96 overflow-auto rounded-md bg-muted p-2 font-mono text-xs"
								aria-label={m.runs_output()}><LogText text={output.output} /></pre>
						{:else}
							<p role="status" class="text-muted-foreground">{m.loading()}</p>
						{/if}
					{/if}
				</li>
			{/each}
		</ul>
	{/if}
</section>
