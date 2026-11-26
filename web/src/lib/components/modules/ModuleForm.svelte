<script lang="ts">
	import type { ModuleSetting } from '#lib/api/gen/sermon/web/v1/module_pb.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { fieldHelp, fieldLabel, moduleLabel } from '#lib/modules/labels.js';
	import {
		configOf,
		fieldsOf,
		placeholderOf,
		problems,
		secretsOf,
		valuesOf,
		type Field,
		type Saved
	} from '#lib/modules/schema.js';
	import { problemText } from '#lib/modules/text.js';
	import { m } from '#lib/paraglide/messages.js';

	/**
	 * The configuration of one module for a group, built from the module's schema (ADR 0039).
	 * Credentials are password fields that start empty: the server never returns a value, only
	 * whether one is set.
	 */
	let {
		moduleId,
		schemaJson,
		setting,
		suggestion = '',
		secretsSet = [],
		secretsWritable = false,
		onSave,
		onCancel
	}: {
		moduleId: string;
		schemaJson: string;
		setting?: ModuleSetting;
		/** The agent's suggestion while nothing is stored. */
		suggestion?: string;
		secretsSet?: string[];
		/** Whether credentials can be saved. */
		secretsWritable?: boolean;
		/** Returns the message of a failure, empty on success. */
		onSave: (saved: Saved) => Promise<string>;
		onCancel: () => void;
	} = $props();

	const id = $props.id();
	const control =
		'rounded-md border border-input bg-background px-2 py-1.5 text-sm focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none';

	// svelte-ignore state_referenced_locally
	const fields = fieldsOf(schemaJson);
	// svelte-ignore state_referenced_locally
	let values = $state(valuesOf(fields, setting?.configJson ?? '', suggestion));
	// svelte-ignore state_referenced_locally
	let enabled = $state(setting ? setting.enabled : true);
	// svelte-ignore state_referenced_locally
	let interval = $state(setting?.interval ? String(Number(setting.interval.seconds)) : '');
	let tried = $state(false);
	let busy = $state(false);
	let failure = $state('');

	const found = $derived(problems(fields, values, secretsSet));
	const intervalProblem = $derived.by(() => {
		const s = interval.trim();
		if (s === '') return false;
		const n = Number(s);
		return !/^\d+$/.test(s) || n < 10 || n > 3600;
	});

	const isList = (f: Field) => f.kind === 'strings' || f.kind === 'integers';

	async function submit(event: SubmitEvent) {
		event.preventDefault();
		tried = true;
		if (found.size > 0 || intervalProblem) return;
		busy = true;
		failure = await onSave({
			enabled,
			intervalSeconds: interval.trim() === '' ? undefined : Number(interval),
			configJson: configOf(fields, values),
			secrets: secretsWritable ? secretsOf(fields, values) : {}
		});
		busy = false;
	}
</script>

<form class="flex flex-col gap-4 rounded-md border border-border p-4" onsubmit={submit} novalidate>
	<h3 class="font-medium">{moduleLabel(moduleId)}</h3>
	{#if failure}
		<p role="alert" class="rounded-md border border-destructive/50 p-3 text-sm text-destructive">
			{failure}
		</p>
	{/if}
	<label class="flex items-center gap-2 text-sm">
		<input type="checkbox" bind:checked={enabled} class="size-4 accent-primary" />
		{m.modules_enabled_label()}
	</label>
	<div class="flex max-w-xs flex-col gap-1.5">
		<Label for="{id}-interval">{m.modules_interval()}</Label>
		<Input
			id="{id}-interval"
			bind:value={interval}
			inputmode="numeric"
			autocomplete="off"
			aria-invalid={tried && intervalProblem}
			aria-describedby="{id}-interval-hint"
		/>
		<p
			id="{id}-interval-hint"
			class="text-xs {tried && intervalProblem ? 'text-destructive' : 'text-muted-foreground'}"
		>
			{tried && intervalProblem ? m.modules_problem_interval() : m.modules_interval_help()}
		</p>
	</div>

	{#each fields as field (field.name)}
		{@const problem = tried ? found.get(field.name) : undefined}
		{@const help = fieldHelp(moduleId, field)}
		{@const fid = `${id}-${field.name}`}
		{@const described = [
			help || isList(field) ? `${fid}-help` : '',
			problem ? `${fid}-problem` : ''
		]
			.filter(Boolean)
			.join(' ')}
		<div class="flex flex-col gap-1.5">
			{#if field.kind === 'boolean'}
				<label class="flex items-center gap-2 text-sm">
					<input
						type="checkbox"
						bind:checked={values[field.name] as boolean}
						class="size-4 accent-primary"
						aria-describedby={described || undefined}
					/>
					{fieldLabel(moduleId, field)}
				</label>
			{:else}
				<Label for={fid}>
					{fieldLabel(moduleId, field)}{field.required ? ' *' : ''}
					{#if field.secret}
						<span class="ml-2 text-xs font-normal text-muted-foreground" data-testid="secret-state">
							{secretsSet.includes(field.name) ? m.modules_secret_set() : m.modules_secret_unset()}
						</span>
					{/if}
				</Label>
				{#if field.secret}
					<Input
						id={fid}
						type="password"
						autocomplete="new-password"
						bind:value={values[field.name] as string}
						placeholder={m.modules_secret_placeholder()}
						disabled={!secretsWritable}
						aria-invalid={problem !== undefined}
						aria-describedby={described || undefined}
					/>
				{:else if field.values}
					<select
						id={fid}
						class="h-9 max-w-xs {control}"
						bind:value={values[field.name] as string}
						aria-invalid={problem !== undefined}
						aria-describedby={described || undefined}
					>
						<option value="">{placeholderOf(field) || '–'}</option>
						{#each field.values as v (v)}
							<option value={v}>{v}</option>
						{/each}
					</select>
				{:else if isList(field)}
					<textarea
						id={fid}
						rows="3"
						class="font-mono {control}"
						bind:value={values[field.name] as string}
						spellcheck="false"
						aria-invalid={problem !== undefined}
						aria-describedby={described || undefined}></textarea>
				{:else}
					<Input
						id={fid}
						bind:value={values[field.name] as string}
						placeholder={placeholderOf(field)}
						inputmode={field.kind === 'string' ? undefined : 'decimal'}
						autocomplete="off"
						spellcheck={false}
						aria-invalid={problem !== undefined}
						aria-describedby={described || undefined}
					/>
				{/if}
			{/if}
			{#if help || isList(field) || (field.secret && !secretsWritable)}
				<p id="{fid}-help" class="text-xs text-muted-foreground">
					{help ?? ''}{isList(field) ? ` ${m.modules_list_help()}` : ''}{field.secret &&
					!secretsWritable
						? ` ${m.modules_secret_unavailable()}`
						: ''}
				</p>
			{/if}
			{#if problem}
				<p id="{fid}-problem" class="text-xs text-destructive">{problemText(problem)}</p>
			{/if}
		</div>
	{/each}

	<div class="flex gap-2">
		<Button type="submit" size="sm" disabled={busy}>{m.modules_save()}</Button>
		<Button type="button" variant="ghost" size="sm" onclick={onCancel}>{m.modules_cancel()}</Button>
	</div>
</form>
