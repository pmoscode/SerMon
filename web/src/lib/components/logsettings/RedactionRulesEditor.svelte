<script lang="ts">
	import Plus from '@lucide/svelte/icons/plus';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import { adminApi } from '#lib/api/admin.js';
	import LogText from '#lib/components/logs/LogText.svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { logSettingsError } from '#lib/logsettings/errors.js';
	import { MAX_RULES, ruleProblem } from '#lib/logsettings/form.js';
	import { m } from '#lib/paraglide/messages.js';

	/**
	 * The redaction rules of the organization, on top of the built-in ones, and a test against a
	 * sample line: it shows what the agents would send with the rules as they are in the form.
	 */
	let {
		rules,
		onSave
	}: {
		rules: string[];
		/** Returns the message of a failure, empty on success. */
		onSave: (rules: string[]) => Promise<string>;
	} = $props();

	// svelte-ignore state_referenced_locally
	let draft = $state<string[]>([...rules]);
	let sample = $state('');
	let tested = $state<string | undefined>(undefined);
	let testFailure = $state('');
	let failure = $state('');
	let saved = $state(false);
	let busy = $state(false);
	let tried = $state(false);

	const active = $derived(draft.map((r) => r.trim()).filter(Boolean));
	const rowProblems = $derived(
		draft.map((r) => (r.trim() === '' ? undefined : ruleProblem(r.trim())))
	);
	const changed = $derived(active.join('\u0000') !== rules.join('\u0000'));

	async function test() {
		testFailure = '';
		tested = undefined;
		try {
			const res = await adminApi.logSettings.testRedaction({
				rules: active.map((pattern) => ({ pattern })),
				sample
			});
			tested = res.redacted;
		} catch (error) {
			testFailure = logSettingsError(error);
		}
	}

	async function save() {
		tried = true;
		saved = false;
		if (rowProblems.some(Boolean)) return;
		busy = true;
		failure = await onSave(active);
		saved = failure === '';
		busy = false;
	}
</script>

<section class="flex flex-col gap-3" aria-labelledby="redaction-title">
	<h2 id="redaction-title" class="text-lg font-medium">{m.logsettings_rules_title()}</h2>
	<p class="text-sm text-muted-foreground">{m.logsettings_rules_text()}</p>
	{#if failure}
		<p role="alert" class="rounded-md border border-destructive/50 p-3 text-sm text-destructive">
			{failure}
		</p>
	{/if}
	{#if saved && !changed}
		<p role="status" class="text-sm">{m.logsettings_saved()}</p>
	{/if}
	<ol class="flex flex-col gap-2">
		{#each draft.keys() as i (i)}
			{@const problem = tried ? rowProblems[i] : undefined}
			<li class="flex items-start gap-2">
				<div class="flex flex-1 flex-col gap-1">
					<Label for="rule-{i}" class="sr-only">{m.logsettings_rule({ n: i + 1 })}</Label>
					<Input
						id="rule-{i}"
						bind:value={draft[i]}
						class="font-mono"
						placeholder={'customer-\\d{6}'}
						spellcheck={false}
						autocomplete="off"
						aria-invalid={problem !== undefined}
					/>
					{#if problem === 'long'}<p class="text-xs text-destructive">
							{m.logsettings_rule_long()}
						</p>{/if}
				</div>
				<Button
					type="button"
					variant="ghost"
					size="icon-sm"
					aria-label={m.logsettings_rule_remove({ n: i + 1 })}
					onclick={() => draft.splice(i, 1)}
				>
					<Trash2 aria-hidden="true" />
				</Button>
			</li>
		{/each}
	</ol>
	<div class="flex flex-wrap gap-2">
		<Button
			type="button"
			variant="outline"
			size="sm"
			disabled={draft.length >= MAX_RULES}
			onclick={() => draft.push('')}
		>
			<Plus aria-hidden="true" />
			{m.logsettings_rule_add()}
		</Button>
		<Button type="button" size="sm" disabled={busy || !changed} onclick={save}>
			{busy ? m.logsettings_saving() : m.logsettings_rules_save()}
		</Button>
	</div>

	<div class="flex flex-col gap-2 rounded-md border border-border p-3">
		<Label for="redaction-sample">{m.logsettings_test_sample()}</Label>
		<textarea
			id="redaction-sample"
			rows="2"
			class="rounded-md border border-input bg-background px-2 py-1.5 font-mono text-sm focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
			bind:value={sample}
			spellcheck="false"
			placeholder="login failed for customer-123456, password=hunter2"></textarea>
		<Button type="button" variant="outline" size="sm" class="self-start" onclick={test}>
			{m.logsettings_test()}
		</Button>
		<div aria-live="polite">
			{#if testFailure}
				<p class="text-sm text-destructive">{testFailure}</p>
			{:else if tested !== undefined}
				<p class="text-sm text-muted-foreground">{m.logsettings_test_result()}</p>
				<p class="rounded-md bg-muted p-2 font-mono text-sm" data-testid="redaction-result">
					<LogText text={tested} />
				</p>
			{/if}
		</div>
	</div>
</section>
