<script lang="ts">
	import { untrack } from 'svelte';
	import { alertingApi } from '#lib/api/alerting.js';
	import {
		AlertRuleChangeAction,
		type ImportAlertRulesResponse
	} from '#lib/api/gen/sermon/web/v1/alert_rule_service_pb.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { notifyError } from '#lib/notify/notify.js';
	import { m } from '#lib/paraglide/messages.js';
	import { withStepUp } from '#lib/stores/stepup.svelte.js';
	import DiffView from './DiffView.svelte';

	/**
	 * Edits or imports rule documents (YAML). "Check" shows problems and the changes without
	 * storing; "Save" stores all documents or none. Rules are typed documents, never queries.
	 */
	let {
		title,
		initial = '',
		file = false,
		onDone,
		onCancel
	}: {
		title: string;
		initial?: string;
		file?: boolean;
		onDone: () => void;
		onCancel: () => void;
	} = $props();

	let text = $state(untrack(() => initial));
	let result = $state<ImportAlertRulesResponse | undefined>(undefined);
	let failure = $state('');
	let busy = $state(false);

	async function run(dryRun: boolean) {
		busy = true;
		failure = '';
		try {
			const res = await withStepUp(() =>
				alertingApi.rules.importAlertRules({ yaml: text, dryRun })
			);
			result = res;
			if (res.applied) onDone();
		} catch (error) {
			failure = notifyError(error);
		} finally {
			busy = false;
		}
	}

	async function load(event: Event) {
		const input = event.currentTarget as HTMLInputElement;
		const f = input.files?.[0];
		if (f) text = await f.text();
		result = undefined;
	}

	function actionText(a: AlertRuleChangeAction): string {
		switch (a) {
			case AlertRuleChangeAction.CREATE:
				return m.rules_action_create();
			case AlertRuleChangeAction.UPDATE:
				return m.rules_action_update();
			default:
				return m.rules_action_unchanged();
		}
	}
</script>

<section class="flex flex-col gap-3 rounded-lg border p-4" aria-label={title}>
	<h2 class="font-medium">{title}</h2>
	{#if file}
		<div class="flex flex-col gap-1">
			<Label for="rules-file">{m.rules_file()}</Label>
			<input
				id="rules-file"
				type="file"
				accept=".yaml,.yml,text/yaml"
				class="text-sm"
				onchange={load}
			/>
		</div>
	{/if}
	<div class="flex flex-col gap-1">
		<Label for="rules-yaml">{m.rules_yaml()}</Label>
		<textarea
			id="rules-yaml"
			rows="18"
			spellcheck="false"
			class="rounded-md border border-input bg-background px-2 py-1 font-mono text-xs focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
			bind:value={text}
			oninput={() => (result = undefined)}></textarea>
	</div>
	{#if failure}<p role="alert" class="text-sm text-destructive">{failure}</p>{/if}
	{#if result && result.problems.length > 0}
		<div
			role="alert"
			class="flex flex-col gap-1 rounded-md border border-destructive/50 p-3 text-sm text-destructive"
		>
			<p class="font-medium">{m.rules_problems()}</p>
			<ul class="list-disc pl-5">
				{#each result.problems as p, i (i)}
					<li>
						{m.rules_problem({
							document: String(p.document),
							line: String(p.line),
							message: p.message
						})}
					</li>
				{/each}
			</ul>
		</div>
	{:else if result && !result.applied}
		<div class="flex flex-col gap-3" role="status">
			<p class="text-sm">{m.rules_checked()}</p>
			{#each result.changes as c (c.ruleId)}
				<div class="flex flex-col gap-1">
					<p class="text-sm"><span class="font-mono">{c.ruleId}</span> – {actionText(c.action)}</p>
					{#if c.action !== AlertRuleChangeAction.UNCHANGED}
						<DiffView
							before={c.beforeYaml}
							after={c.afterYaml}
							label={m.rules_diff_label({ rule: c.ruleId })}
						/>
					{/if}
				</div>
			{/each}
		</div>
	{/if}
	<div class="flex flex-wrap gap-2">
		<Button variant="outline" disabled={busy || text.trim() === ''} onclick={() => run(true)}
			>{m.rules_check()}</Button
		>
		<Button disabled={busy || text.trim() === ''} onclick={() => run(false)}
			>{m.rules_save()}</Button
		>
		<Button variant="ghost" onclick={onCancel}>{m.notify_cancel()}</Button>
	</div>
</section>
