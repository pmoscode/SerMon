<script lang="ts">
	import { durationFromMs, timestampMs, type Timestamp } from '@bufbuild/protobuf/wkt';
	import { Code, ConnectError } from '@connectrpc/connect';
	import X from '@lucide/svelte/icons/x';
	import { kioskTokensApi } from '#lib/api/kiosk.js';
	import type { KioskToken } from '#lib/api/gen/sermon/web/v1/kiosk_pb.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { kioskUrl } from '#lib/kiosk/fragment.js';
	import { displayText } from '#lib/logs/text.js';
	import { m } from '#lib/paraglide/messages.js';
	import { withStepUp } from '#lib/stores/stepup.svelte.js';

	/** The kiosk tokens of a shared dashboard (M5-26), for admins of all groups. */
	let { dashboardId }: { dashboardId: string } = $props();

	let tokens = $state.raw<KioskToken[]>([]);
	let name = $state('');
	let days = $state(90);
	let busy = $state(false);
	let failure = $state('');
	let address = $state('');
	let copied = $state(false);

	const date = $derived(
		new Intl.DateTimeFormat(localeState.current, { dateStyle: 'medium', timeStyle: 'short' })
	);
	const at = (t?: Timestamp) => (t ? date.format(timestampMs(t)) : '');

	function explain(error: unknown): string {
		const err = ConnectError.from(error);
		return err.code === Code.FailedPrecondition
			? m.kiosk_token_share_first()
			: m.kiosk_token_failed({ reason: err.rawMessage });
	}

	async function load() {
		try {
			tokens = (await kioskTokensApi.listKioskTokens({ dashboardId })).tokens;
		} catch (error) {
			failure = explain(error);
		}
	}
	void load();

	async function create(event: SubmitEvent) {
		event.preventDefault();
		busy = true;
		failure = '';
		address = '';
		try {
			const res = await withStepUp(() =>
				kioskTokensApi.createKioskToken({
					dashboardId,
					name: name.trim(),
					validFor: durationFromMs(Math.min(365, Math.max(1, Math.round(days))) * 86_400_000)
				})
			);
			address = kioskUrl(location.origin, [res.secret]);
			name = '';
			await load();
		} catch (error) {
			failure = explain(error);
		} finally {
			busy = false;
		}
	}

	async function revoke(id: string) {
		busy = true;
		failure = '';
		try {
			await kioskTokensApi.revokeKioskToken({ tokenId: id });
			await load();
		} catch (error) {
			failure = explain(error);
		} finally {
			busy = false;
		}
	}

	async function copy() {
		await navigator.clipboard.writeText(address);
		copied = true;
	}

	const now = Date.now();
	const open = (t: KioskToken) =>
		!t.revokeTime && (t.expireTime ? timestampMs(t.expireTime) > now : false);
</script>

<section class="flex flex-col gap-3 rounded-lg border p-4" aria-labelledby="kiosk-tokens-title">
	<h2 id="kiosk-tokens-title" class="font-medium">{m.kiosk_tokens()}</h2>
	<p class="text-sm text-muted-foreground">{m.kiosk_tokens_text()}</p>
	<form class="flex flex-wrap items-end gap-2" onsubmit={create}>
		<div class="flex flex-col gap-1">
			<Label for="kiosk-name">{m.kiosk_token_name()}</Label>
			<Input id="kiosk-name" class="w-56" maxlength={100} bind:value={name} required />
		</div>
		<div class="flex flex-col gap-1">
			<Label for="kiosk-days">{m.kiosk_token_days()}</Label>
			<Input id="kiosk-days" class="w-24" type="number" min="1" max="365" bind:value={days} />
		</div>
		<Button type="submit" disabled={busy || !name.trim()}>{m.kiosk_token_create()}</Button>
	</form>
	{#if address}
		<div class="flex flex-col gap-1 rounded-md border border-amber-500 p-3" role="status">
			<p class="text-sm">{m.kiosk_token_created()}</p>
			<Label for="kiosk-address">{m.kiosk_token_address()}</Label>
			<div class="flex gap-2">
				<Input id="kiosk-address" class="font-mono text-xs" readonly value={address} />
				<Button variant="outline" onclick={copy}
					>{copied ? m.kiosk_token_copied() : m.kiosk_token_copy()}</Button
				>
			</div>
		</div>
	{/if}
	{#if failure}
		<p role="alert" class="text-sm text-destructive">{failure}</p>
	{/if}
	{#if tokens.length === 0}
		<p class="text-sm text-muted-foreground">{m.kiosk_tokens_none()}</p>
	{:else}
		<ul class="flex flex-col divide-y text-sm" aria-label={m.kiosk_tokens()}>
			{#each tokens as t (t.id)}
				<li class="flex flex-wrap items-center gap-x-3 py-1.5">
					<span class="font-medium">{displayText(t.name)}</span>
					<span class="text-muted-foreground">
						{m.kiosk_token_state({ name: displayText(t.createdByName), until: at(t.expireTime) })}
						{#if t.lastUseTime}· {m.kiosk_token_used({ time: at(t.lastUseTime) })}{/if}
						{#if t.revokeTime}· {m.kiosk_token_revoked()}{:else if !open(t)}· {m.kiosk_token_expired()}{/if}
					</span>
					{#if open(t)}
						<Button
							variant="ghost"
							size="icon-sm"
							class="ml-auto"
							disabled={busy}
							aria-label={m.kiosk_token_revoke({ name: t.name })}
							onclick={() => revoke(t.id)}
						>
							<X aria-hidden="true" />
						</Button>
					{/if}
				</li>
			{/each}
		</ul>
	{/if}
</section>
