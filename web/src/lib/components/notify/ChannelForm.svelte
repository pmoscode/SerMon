<script lang="ts">
	import { untrack } from 'svelte';
	import {
		ChannelFieldKind,
		type ChannelType,
		type NotificationChannel
	} from '#lib/api/gen/sermon/web/v1/notification_pb.js';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { channelFieldName, channelTypeName } from '#lib/notify/notify.js';
	import { m } from '#lib/paraglide/messages.js';

	export interface ChannelInput {
		type: string;
		name: string;
		language: string;
		enabled: boolean;
		settings: Record<string, string>;
		/** Typed secrets; empty ones keep the stored value. */
		secrets: Record<string, string>;
	}

	/**
	 * The form of a channel, built from the fields of its type. Secrets are write-only: the form
	 * never shows a stored secret, an empty secret field keeps it.
	 */
	let {
		types,
		channel,
		busy,
		onSubmit,
		onCancel
	}: {
		types: ChannelType[];
		channel?: NotificationChannel;
		busy: boolean;
		onSubmit: (input: ChannelInput) => void;
		onCancel: () => void;
	} = $props();

	// The form starts from the channel once; later changes of the prop don't reset typing.
	const initial = untrack(() => $state.snapshot(channel));
	let type = $state(initial?.type ?? untrack(() => types[0]?.type) ?? '');
	let name = $state(initial?.name ?? '');
	let language = $state(initial?.language ?? 'de');
	let enabled = $state(initial?.enabled ?? true);
	let settings = $state<Record<string, string>>({ ...(initial?.settings ?? {}) });
	let secrets = $state<Record<string, string>>({});

	const fields = $derived(types.find((t) => t.type === type)?.fields ?? []);
	const editing = $derived(!!initial);
	const stored = (field: string) => initial?.secretNames.includes(field) ?? false;
	const complete = $derived(
		name.trim() !== '' &&
			fields.every(
				(f) =>
					!f.required ||
					(f.secret
						? (secrets[f.name] ?? '') !== '' || stored(f.name)
						: (settings[f.name] ?? '') !== '')
			)
	);

	function inputType(kind: ChannelFieldKind, secret: boolean): string {
		if (secret) return 'password';
		if (kind === ChannelFieldKind.URL) return 'url';
		if (kind === ChannelFieldKind.NUMBER) return 'number';
		return 'text';
	}

	function submit(event: SubmitEvent) {
		event.preventDefault();
		const known = new Set(fields.map((f) => f.name));
		const pick = (values: Record<string, string>, secret: boolean) =>
			Object.fromEntries(
				Object.entries(values)
					.filter(
						([k, v]) =>
							known.has(k) && v.trim() !== '' && fields.find((f) => f.name === k)?.secret === secret
					)
					.map(([k, v]) => [k, v.trim()])
			);
		onSubmit({
			type,
			name: name.trim(),
			language,
			enabled,
			settings: pick(settings, false),
			secrets: pick(secrets, true)
		});
	}
</script>

<form
	class="flex flex-col gap-4 rounded-lg border p-4"
	onsubmit={submit}
	aria-label={editing ? m.channel_edit() : m.channel_new()}
>
	<div class="flex flex-wrap items-end gap-3">
		<div class="flex flex-col gap-1">
			<Label for="channel-type">{m.channel_type()}</Label>
			<div class="flex items-center gap-1">
				<select
					id="channel-type"
					class="h-9 rounded-md border border-input bg-background px-2 text-sm focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none disabled:opacity-60"
					bind:value={type}
					disabled={editing}
				>
					{#each types as t (t.type)}
						<option value={t.type}>{channelTypeName(t.type)}</option>
					{/each}
				</select>
				{#if type}
					<HelpTip topic={`channels/${type}`} />
				{/if}
			</div>
		</div>
		<div class="flex flex-col gap-1">
			<Label for="channel-name">{m.channel_name()}</Label>
			<Input id="channel-name" class="w-64" maxlength={100} bind:value={name} />
		</div>
		<div class="flex flex-col gap-1">
			<Label for="channel-language">{m.channel_language()}</Label>
			<select
				id="channel-language"
				class="h-9 rounded-md border border-input bg-background px-2 text-sm focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
				bind:value={language}
			>
				<option value="de">Deutsch</option>
				<option value="en">English</option>
			</select>
		</div>
		<label class="flex h-9 items-center gap-2 text-sm">
			<input type="checkbox" bind:checked={enabled} class="size-4" />
			{m.channel_enabled()}
		</label>
	</div>

	<div class="grid gap-3 sm:grid-cols-2">
		{#each fields as f (`${type}-${f.name}`)}
			<div class="flex flex-col gap-1">
				<Label for={`channel-field-${f.name}`}>
					{channelFieldName(f.name)}{f.required ? '' : ` (${m.channel_optional()})`}
				</Label>
				{#if f.kind === ChannelFieldKind.CHOICE && !f.secret}
					<select
						id={`channel-field-${f.name}`}
						class="h-9 rounded-md border border-input bg-background px-2 text-sm focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
						bind:value={settings[f.name]}
					>
						{#if !f.required}<option value=""></option>{/if}
						{#each f.choices as c (c)}<option value={c}>{c}</option>{/each}
					</select>
				{:else if f.secret}
					<Input
						id={`channel-field-${f.name}`}
						type="password"
						autocomplete="off"
						maxlength={f.maxLength}
						placeholder={stored(f.name) ? m.channel_secret_kept() : ''}
						bind:value={secrets[f.name]}
					/>
				{:else}
					<Input
						id={`channel-field-${f.name}`}
						type={inputType(f.kind, false)}
						maxlength={f.maxLength}
						bind:value={settings[f.name]}
					/>
				{/if}
			</div>
		{/each}
	</div>
	{#if editing}
		<p class="text-xs text-muted-foreground">{m.channel_secrets_note()}</p>
	{/if}

	<div class="flex gap-2">
		<Button type="submit" disabled={busy || !complete}
			>{editing ? m.admin_save() : m.channel_create()}</Button
		>
		<Button type="button" variant="ghost" onclick={onCancel}>{m.notify_cancel()}</Button>
	</div>
</form>
