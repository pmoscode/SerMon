<script lang="ts">
	import Fingerprint from '@lucide/svelte/icons/fingerprint';
	import { timestampDate } from '@bufbuild/protobuf/wkt';
	import { Code, codeOf } from '#lib/api/client.js';
	import { adminApi } from '#lib/api/admin.js';
	import type { Passkey } from '#lib/api/gen/sermon/web/v1/passkey_pb.js';
	import { CeremonyError, create, supported } from '#lib/auth/webauthn.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Card from '#lib/components/ui/card/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { formatTimestamp } from '#lib/format/format.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { m } from '#lib/paraglide/messages.js';
	import { sessionState } from '#lib/stores/session.svelte.js';
	import { needsStepUp, withStepUp } from '#lib/stores/stepup.svelte.js';

	/**
	 * The caller's passkeys (M3-24): add one (after a step-up), rename, delete (after a step-up).
	 * Names come from the user and are shown as text.
	 */
	let keys = $state<Passkey[]>([]);
	let status = $state<'loading' | 'ready' | 'error'>('loading');
	let name = $state('');
	let busy = $state(false);
	let failure = $state('');
	let notice = $state('');
	let editing = $state<string | undefined>();
	let editName = $state('');
	let confirmDelete = $state<string | undefined>();

	const available = $derived(sessionState.current?.passkeysAvailable ?? false);
	const browserOk = supported();

	async function load() {
		try {
			keys = (await adminApi.passkeys.listPasskeys({})).passkeys;
			status = 'ready';
		} catch (error) {
			if (codeOf(error) !== Code.Unauthenticated) status = 'error';
		}
	}

	$effect(() => {
		void load();
	});

	const locale = $derived(localeState.current);
	const when = (t: Passkey['createTime']) =>
		t ? formatTimestamp(timestampDate(t).getTime() / 1000, locale) : '–';

	function message(error: unknown): string {
		if (error instanceof CeremonyError) {
			if (error.reason === 'cancelled') return '';
			return error.reason === 'unsupported' ? m.passkey_unsupported() : m.error_unavailable();
		}
		if (needsStepUp(error)) return '';
		switch (codeOf(error)) {
			case Code.ResourceExhausted:
				return m.passkey_too_many();
			case Code.InvalidArgument:
				return m.passkey_name_invalid();
			case Code.PermissionDenied:
				return m.passkey_refused();
			default:
				return m.error_unavailable();
		}
	}

	async function run(action: () => Promise<void>) {
		busy = true;
		failure = '';
		notice = '';
		try {
			await action();
			await sessionState.refresh();
			await load();
		} catch (error) {
			failure = message(error);
		} finally {
			busy = false;
		}
	}

	async function add(event: SubmitEvent) {
		event.preventDefault();
		const n = name.trim();
		if (!n) return;
		await run(async () => {
			const begun = await withStepUp(() => adminApi.passkeys.beginPasskeyRegistration({ name: n }));
			const credentialJson = await create(begun.optionsJson);
			await adminApi.passkeys.finishPasskeyRegistration({
				ceremonyId: begun.ceremonyId,
				credentialJson
			});
			name = '';
			notice = m.passkey_added({ name: n });
		});
	}

	async function rename(event: SubmitEvent, id: string) {
		event.preventDefault();
		await run(async () => {
			await adminApi.passkeys.renamePasskey({ id, name: editName.trim() });
			editing = undefined;
		});
	}

	async function remove(id: string) {
		await run(async () => {
			await withStepUp(() => adminApi.passkeys.deletePasskey({ id }));
			confirmDelete = undefined;
		});
	}
</script>

{#if available}
	<Card.Root class="max-w-2xl">
		<Card.Header>
			<Card.Title>
				<span class="flex items-center gap-2"
					><Fingerprint class="size-5" aria-hidden="true" />{m.passkey_title()}</span
				>
			</Card.Title>
			<Card.Description>{m.passkey_text()}</Card.Description>
		</Card.Header>
		<Card.Content class="flex flex-col gap-4">
			{#if !browserOk}
				<p class="rounded-md border border-dashed p-3 text-sm">{m.passkey_unsupported()}</p>
			{/if}
			{#if failure}
				<p
					role="alert"
					class="rounded-md border border-destructive/50 p-3 text-sm text-destructive"
				>
					{failure}
				</p>
			{/if}
			{#if notice}
				<p role="status" class="rounded-md border bg-muted p-3 text-sm">{notice}</p>
			{/if}
			{#if status === 'loading'}
				<p role="status" class="text-sm text-muted-foreground">{m.loading()}</p>
			{:else if status === 'error'}
				<p role="alert" class="text-sm text-destructive">{m.error_unavailable()}</p>
			{:else}
				<ul class="divide-y rounded-lg border" aria-label={m.passkey_title()}>
					{#each keys as key (key.id)}
						<li class="flex flex-wrap items-center justify-between gap-2 px-3 py-2">
							{#if editing === key.id}
								<form class="flex flex-wrap items-center gap-2" onsubmit={(e) => rename(e, key.id)}>
									<Label for="rename-{key.id}" class="sr-only">{m.passkey_name()}</Label>
									<Input id="rename-{key.id}" class="w-56" bind:value={editName} maxlength={64} />
									<Button type="submit" size="sm" disabled={busy || !editName.trim()}
										>{m.admin_save()}</Button
									>
									<Button
										type="button"
										size="sm"
										variant="ghost"
										onclick={() => (editing = undefined)}>{m.stepup_cancel()}</Button
									>
								</form>
							{:else}
								<div class="min-w-0">
									<p class="flex items-center gap-1 font-medium">
										<span class="truncate">{key.name}</span>
										{#if key.synced}<span class="rounded bg-muted px-1 text-xs font-normal"
												>{m.passkey_synced()}</span
											>{/if}
									</p>
									<p class="text-xs text-muted-foreground">
										{m.passkey_added_at({ at: when(key.createTime) })} ·
										{key.lastUseTime
											? m.passkey_last_used({ at: when(key.lastUseTime) })
											: m.passkey_never_used()}
									</p>
								</div>
								<span class="flex gap-1">
									<Button
										size="sm"
										variant="ghost"
										aria-label={m.passkey_rename({ name: key.name })}
										onclick={() => {
											editing = key.id;
											editName = key.name;
										}}>{m.admin_rename()}</Button
									>
									{#if confirmDelete === key.id}
										<Button
											size="sm"
											variant="destructive"
											disabled={busy}
											onclick={() => remove(key.id)}>{m.admin_delete_confirm()}</Button
										>
										<Button size="sm" variant="ghost" onclick={() => (confirmDelete = undefined)}
											>{m.stepup_cancel()}</Button
										>
									{:else}
										<Button
											size="sm"
											variant="ghost"
											aria-label={m.passkey_delete({ name: key.name })}
											onclick={() => (confirmDelete = key.id)}>{m.admin_delete()}</Button
										>
									{/if}
								</span>
							{/if}
						</li>
					{:else}
						<li class="px-3 py-4 text-sm text-muted-foreground">{m.passkey_list_empty()}</li>
					{/each}
				</ul>
				{#if browserOk}
					<form class="flex flex-wrap items-end gap-2" onsubmit={add}>
						<div class="flex flex-col gap-1">
							<Label for="passkey-name">{m.passkey_name()}</Label>
							<Input
								id="passkey-name"
								class="w-64"
								bind:value={name}
								maxlength={64}
								placeholder={m.passkey_name_placeholder()}
							/>
						</div>
						<Button type="submit" disabled={busy || !name.trim()}>{m.passkey_add()}</Button>
					</form>
				{/if}
			{/if}
		</Card.Content>
	</Card.Root>
{/if}
