<script lang="ts">
	import { alertingApi } from '#lib/api/alerting.js';
	import {
		DeliveryStatus,
		type ChannelType,
		type NotificationChannel
	} from '#lib/api/gen/sermon/web/v1/notification_pb.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { secretUpdate } from '#lib/notify/forms.js';
	import { channelFieldName, channelTypeName, notifyError } from '#lib/notify/notify.js';
	import { m } from '#lib/paraglide/messages.js';
	import { withStepUp } from '#lib/stores/stepup.svelte.js';
	import ChannelForm, { type ChannelInput } from './ChannelForm.svelte';

	/**
	 * The channels of the organization (M2-18): create, change, switch off, test, delete.
	 * Secrets are write-only – the list names the secrets that are set, never their values.
	 */
	let { onChange = () => {} }: { onChange?: () => void } = $props();

	let types = $state<ChannelType[]>([]);
	let channels = $state<NotificationChannel[]>([]);
	let status = $state<'loading' | 'ready' | 'error'>('loading');
	let failure = $state('');
	let busy = $state(false);
	let editing = $state<NotificationChannel | 'new' | undefined>(undefined);
	let confirmDelete = $state('');
	let tests = $state<Record<string, { ok: boolean; text: string }>>({});

	async function load() {
		try {
			const [t, c] = await Promise.all([
				alertingApi.channels.listChannelTypes({}),
				alertingApi.channels.listChannels({})
			]);
			types = t.types;
			channels = c.channels;
			status = 'ready';
		} catch {
			status = 'error';
		}
	}

	$effect(() => {
		void load();
	});

	async function run(action: () => Promise<unknown>): Promise<boolean> {
		busy = true;
		failure = '';
		try {
			await withStepUp(action);
			await load();
			onChange();
			return true;
		} catch (error) {
			failure = notifyError(error);
			return false;
		} finally {
			busy = false;
		}
	}

	async function save(input: ChannelInput) {
		const current = editing;
		const ok = await run(() =>
			current === 'new' || !current
				? alertingApi.channels.createChannel(input)
				: alertingApi.channels.updateChannel({
						channelId: current.id,
						name: input.name,
						language: input.language,
						enabled: input.enabled,
						replaceSettings: true,
						settings: input.settings,
						secrets: secretUpdate(input.secrets)
					})
		);
		if (ok) editing = undefined;
	}

	const toggle = (c: NotificationChannel) =>
		run(() => alertingApi.channels.updateChannel({ channelId: c.id, enabled: !c.enabled }));

	async function remove(c: NotificationChannel) {
		if (confirmDelete !== c.id) {
			confirmDelete = c.id;
			return;
		}
		confirmDelete = '';
		await run(() => alertingApi.channels.deleteChannel({ channelId: c.id }));
	}

	async function test(c: NotificationChannel) {
		failure = '';
		try {
			const res = await alertingApi.channels.testChannel({ channelId: c.id });
			const sent = res.delivery?.status === DeliveryStatus.SENT;
			tests = {
				...tests,
				[c.id]: {
					ok: sent,
					text: sent
						? m.channel_test_sent()
						: m.channel_test_failed({ error: res.delivery?.error || '–' })
				}
			};
		} catch (error) {
			tests = { ...tests, [c.id]: { ok: false, text: notifyError(error) } };
		}
	}
</script>

<div class="flex flex-col gap-4">
	{#if status === 'loading'}
		<p role="status" class="text-muted-foreground">{m.loading()}</p>
	{:else if status === 'error'}
		<div class="flex items-center gap-3">
			<p role="alert" class="text-destructive">{m.error_unavailable()}</p>
			<Button variant="outline" onclick={load}>{m.retry()}</Button>
		</div>
	{:else}
		{#if failure}
			<p role="alert" class="rounded-md border border-destructive/50 p-3 text-sm text-destructive">
				{failure}
			</p>
		{/if}
		{#if editing}
			<ChannelForm
				{types}
				channel={editing === 'new' ? undefined : editing}
				{busy}
				onSubmit={save}
				onCancel={() => (editing = undefined)}
			/>
		{:else}
			<Button class="self-start" onclick={() => (editing = 'new')} disabled={types.length === 0}
				>{m.channel_new()}</Button
			>
		{/if}
		<ul class="divide-y rounded-lg border" aria-label={m.channels_title()}>
			{#each channels as c (c.id)}
				<li class="flex flex-col gap-2 px-3 py-3 text-sm">
					<div class="flex flex-wrap items-center justify-between gap-2">
						<span>
							<span class="font-medium">{c.name}</span>
							<span class="text-muted-foreground"
								>· {channelTypeName(c.type)} · {c.language.toUpperCase()}</span
							>
							{#if !c.enabled}
								<span class="ml-1 rounded-md border px-1.5 py-0.5 text-xs"
									>{m.channel_disabled()}</span
								>
							{/if}
							{#if c.secretNames.length > 0}
								<span class="block text-xs text-muted-foreground">
									{m.channel_secrets_set({ names: c.secretNames.map(channelFieldName).join(', ') })}
								</span>
							{/if}
						</span>
						<span class="flex flex-wrap gap-1">
							<Button size="sm" variant="outline" onclick={() => test(c)}>{m.channel_test()}</Button
							>
							<Button size="sm" variant="ghost" onclick={() => (editing = c)}
								>{m.channel_edit()}</Button
							>
							<Button size="sm" variant="ghost" disabled={busy} onclick={() => toggle(c)}>
								{c.enabled ? m.channel_disable() : m.channel_enable()}
							</Button>
							<Button size="sm" variant="ghost" disabled={busy} onclick={() => remove(c)}>
								{confirmDelete === c.id ? m.admin_delete_confirm() : m.admin_delete()}
							</Button>
						</span>
					</div>
					{#if tests[c.id]}
						<p
							role={tests[c.id].ok ? 'status' : 'alert'}
							class={tests[c.id].ok ? 'text-emerald-700 dark:text-emerald-400' : 'text-destructive'}
						>
							{tests[c.id].text}
						</p>
					{/if}
				</li>
			{:else}
				<li class="px-3 py-4 text-sm text-muted-foreground">{m.channels_none()}</li>
			{/each}
		</ul>
	{/if}
</div>
