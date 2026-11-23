<script lang="ts">
	import Plus from '@lucide/svelte/icons/plus';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import type { MessageInitShape } from '@bufbuild/protobuf';
	import type {
		GroupLogSettings,
		LogSettingsSchema
	} from '#lib/api/gen/sermon/web/v1/log_settings_pb.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { severities } from '#lib/logs/filter.js';
	import { severityNameLabel } from '#lib/logs/labels.js';
	import {
		draftOf,
		MAX_FILES,
		problems,
		settingsOf,
		valid,
		type FileProblem,
		type GroupDraft
	} from '#lib/logsettings/form.js';
	import { m } from '#lib/paraglide/messages.js';

	/** The settings of one group: what its agents send. */
	let {
		group,
		onSave,
		onCancel
	}: {
		group: GroupLogSettings;
		/** Returns the message of a failure, empty on success. */
		onSave: (settings: MessageInitShape<typeof LogSettingsSchema>) => Promise<string>;
		onCancel: () => void;
	} = $props();

	const select =
		'h-9 rounded-md border border-input bg-background px-2 text-sm focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none';
	const id = $props.id();

	// svelte-ignore state_referenced_locally
	let draft = $state<GroupDraft>(draftOf(group.settings));
	let busy = $state(false);
	let failure = $state('');
	let tried = $state(false);

	const found = $derived(problems(draft));
	const fileText: Record<FileProblem, () => string> = {
		empty: () => m.logsettings_glob_empty(),
		absolute: () => m.logsettings_glob_absolute(),
		segments: () => m.logsettings_glob_segments(),
		wildcards: () => m.logsettings_glob_wildcards(),
		long: () => m.logsettings_glob_long(),
		duplicate: () => m.logsettings_glob_duplicate(),
		multiline: () => m.logsettings_multiline_long()
	};

	async function submit(event: SubmitEvent) {
		event.preventDefault();
		tried = true;
		if (!valid(found)) return;
		busy = true;
		failure = await onSave(settingsOf(draft));
		busy = false;
	}
</script>

<form class="flex flex-col gap-4 rounded-md border border-border p-4" onsubmit={submit} novalidate>
	<h3 class="font-medium">{m.logsettings_edit_title({ group: group.groupName })}</h3>
	{#if failure}
		<p role="alert" class="rounded-md border border-destructive/50 p-3 text-sm text-destructive">
			{failure}
		</p>
	{/if}
	<label class="flex items-center gap-2 text-sm">
		<input type="checkbox" bind:checked={draft.enabled} class="size-4 accent-primary" />
		{m.logsettings_enabled()}
	</label>
	<div class="grid gap-4 sm:grid-cols-2">
		<div class="flex flex-col gap-1.5">
			<Label for="{id}-severity">{m.logsettings_min_severity()}</Label>
			<select id="{id}-severity" class={select} bind:value={draft.minSeverity}>
				<option value="">{m.logsettings_all_severities()}</option>
				{#each severities as s (s)}
					<option value={s}>{m.logs_filter_at_least({ severity: severityNameLabel(s) })}</option>
				{/each}
			</select>
		</div>
		<div class="flex flex-col gap-1.5">
			<Label for="{id}-units">{m.logsettings_exclude_units()}</Label>
			<textarea
				id="{id}-units"
				rows="3"
				class="rounded-md border border-input bg-background px-2 py-1.5 font-mono text-sm focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
				bind:value={draft.excludeUnits}
				spellcheck="false"
				aria-invalid={tried && found.units !== undefined}
				aria-describedby="{id}-units-hint"></textarea>
			<p
				id="{id}-units-hint"
				class="text-xs {tried && found.units ? 'text-destructive' : 'text-muted-foreground'}"
			>
				{#if tried && found.units === 'unit'}{m.logsettings_unit_invalid()}{:else if tried && found.units === 'units'}{m.logsettings_units_too_many()}{:else}{m.logsettings_exclude_units_hint()}{/if}
			</p>
		</div>
	</div>

	<fieldset class="flex flex-col gap-2">
		<legend class="mb-1 text-sm font-medium">{m.logsettings_files()}</legend>
		<p class="text-xs text-muted-foreground">{m.logsettings_files_hint()}</p>
		{#each draft.files as file, i (i)}
			{@const problem = tried ? found.files[i] : undefined}
			<div class="grid gap-2 sm:grid-cols-[2fr_1fr_auto] sm:items-start">
				<div class="flex flex-col gap-1">
					<Label for="{id}-pattern-{i}" class="sr-only"
						>{m.logsettings_file_pattern({ n: i + 1 })}</Label
					>
					<Input
						id="{id}-pattern-{i}"
						bind:value={file.pattern}
						placeholder="/var/log/nginx/*.log"
						class="font-mono"
						spellcheck={false}
						autocomplete="off"
						aria-invalid={problem !== undefined && problem !== 'multiline'}
						aria-describedby={problem ? `${id}-problem-${i}` : undefined}
					/>
					{#if problem}
						<p id="{id}-problem-{i}" class="text-xs text-destructive">{fileText[problem]()}</p>
					{/if}
				</div>
				<div class="flex flex-col gap-1">
					<Label for="{id}-multiline-{i}" class="sr-only"
						>{m.logsettings_file_multiline({ n: i + 1 })}</Label
					>
					<Input
						id="{id}-multiline-{i}"
						bind:value={file.multiline}
						placeholder={m.logsettings_multiline_placeholder()}
						class="font-mono"
						spellcheck={false}
						autocomplete="off"
						aria-invalid={problem === 'multiline'}
					/>
				</div>
				<Button
					type="button"
					variant="ghost"
					size="icon-sm"
					aria-label={m.logsettings_file_remove({ n: i + 1 })}
					onclick={() => draft.files.splice(i, 1)}
				>
					<Trash2 aria-hidden="true" />
				</Button>
			</div>
		{/each}
		{#if tried && found.tooManyFiles}
			<p class="text-xs text-destructive">{m.logsettings_files_too_many({ max: MAX_FILES })}</p>
		{/if}
		<Button
			type="button"
			variant="outline"
			size="sm"
			class="self-start"
			disabled={draft.files.length >= MAX_FILES}
			onclick={() => draft.files.push({ pattern: '', multiline: '' })}
		>
			<Plus aria-hidden="true" />
			{m.logsettings_file_add()}
		</Button>
	</fieldset>

	<div class="flex gap-2">
		<Button type="submit" size="sm" disabled={busy}
			>{busy ? m.logsettings_saving() : m.logsettings_save()}</Button
		>
		<Button type="button" variant="ghost" size="sm" onclick={onCancel}
			>{m.logsettings_cancel()}</Button
		>
	</div>
</form>
