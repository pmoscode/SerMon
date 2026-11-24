<script lang="ts">
	import KeyRound from '@lucide/svelte/icons/key-round';
	import { timestampDate } from '@bufbuild/protobuf/wkt';
	import { api, Code, codeOf } from '#lib/api/client.js';
	import type { OidcIdentity } from '#lib/api/gen/sermon/web/v1/auth_pb.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Card from '#lib/components/ui/card/index.js';
	import { formatTimestamp } from '#lib/format/format.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { m } from '#lib/paraglide/messages.js';
	import { sessionState } from '#lib/stores/session.svelte.js';
	import { OidcIntent, ssoState } from '#lib/stores/sso.svelte.js';
	import { needsStepUp, withStepUp } from '#lib/stores/stepup.svelte.js';

	/**
	 * The identities of the OIDC provider linked to the account (M3-22): link one (after a
	 * step-up, at the provider), unlink one while another way to sign in remains.
	 */
	let { taken = false }: { taken?: boolean } = $props();

	let identities = $state<OidcIdentity[]>([]);
	let status = $state<'loading' | 'ready' | 'error'>('loading');
	let failure = $state('');
	let busy = $state(false);
	let confirm = $state<string | undefined>();

	async function load() {
		try {
			const [res] = await Promise.all([api.auth.listOidcIdentities({}), ssoState.load()]);
			identities = res.identities;
			status = 'ready';
		} catch (error) {
			if (codeOf(error) !== Code.Unauthenticated) status = 'error';
		}
	}

	$effect(() => {
		void load();
	});

	const locale = $derived(localeState.current);
	const when = (t: OidcIdentity['createTime']) =>
		t ? formatTimestamp(timestampDate(t).getTime() / 1000, locale) : '–';
	/** Unlinking may not take the last way in. */
	const canUnlink = $derived((sessionState.current?.hasPassword ?? false) || identities.length > 1);

	async function link() {
		failure = '';
		busy = true;
		const result = await ssoState.start(OidcIntent.LINK, '/settings/security');
		if (result) {
			busy = false;
			if (result !== 'step_up') failure = m.sso_error_unavailable();
		}
	}

	async function unlink(issuer: string) {
		failure = '';
		busy = true;
		try {
			await withStepUp(() => api.auth.unlinkOidcIdentity({ issuer }));
			confirm = undefined;
			await sessionState.refresh();
			await load();
		} catch (error) {
			if (needsStepUp(error)) return;
			failure =
				codeOf(error) === Code.FailedPrecondition ? m.sso_unlink_last() : m.error_unavailable();
		} finally {
			busy = false;
		}
	}
</script>

{#if status === 'error'}
	<p role="alert" class="max-w-2xl text-sm text-destructive">{m.error_unavailable()}</p>
{:else if status === 'ready' && (ssoState.enabled || identities.length > 0)}
	<Card.Root class="max-w-2xl">
		<Card.Header>
			<Card.Title>
				<span class="flex items-center gap-2"
					><KeyRound class="size-5" aria-hidden="true" />{m.sso_title()}</span
				>
			</Card.Title>
			<Card.Description>{m.sso_text({ name: ssoState.name || 'SSO' })}</Card.Description>
		</Card.Header>
		<Card.Content class="flex flex-col gap-4">
			{#if taken}
				<p
					role="alert"
					class="rounded-md border border-destructive/50 p-3 text-sm text-destructive"
				>
					{m.sso_taken()}
				</p>
			{/if}
			{#if failure}
				<p
					role="alert"
					class="rounded-md border border-destructive/50 p-3 text-sm text-destructive"
				>
					{failure}
				</p>
			{/if}
			{#if identities.length === 0}
				<p class="text-sm text-muted-foreground">{m.sso_none()}</p>
			{:else}
				<ul class="divide-y rounded-lg border" aria-label={m.sso_title()}>
					{#each identities as id (id.issuer)}
						<li class="flex flex-wrap items-center justify-between gap-2 px-3 py-2">
							<div class="min-w-0">
								<p class="truncate font-medium">{id.email || id.subject}</p>
								<p class="truncate text-xs text-muted-foreground">
									{id.issuer} · {m.sso_linked_at({ at: when(id.createTime) })}
									{#if id.lastLoginTime}· {m.sso_last_login({ at: when(id.lastLoginTime) })}{/if}
								</p>
							</div>
							{#if confirm === id.issuer}
								<span class="flex gap-1">
									<Button
										size="sm"
										variant="destructive"
										disabled={busy}
										onclick={() => unlink(id.issuer)}>{m.sso_unlink_confirm()}</Button
									>
									<Button size="sm" variant="ghost" onclick={() => (confirm = undefined)}
										>{m.stepup_cancel()}</Button
									>
								</span>
							{:else}
								<Button
									size="sm"
									variant="ghost"
									disabled={busy || !canUnlink}
									aria-describedby={canUnlink ? undefined : 'sso-unlink-hint'}
									onclick={() => (confirm = id.issuer)}>{m.sso_unlink()}</Button
								>
							{/if}
						</li>
					{/each}
				</ul>
				{#if !canUnlink}
					<p id="sso-unlink-hint" class="text-xs text-muted-foreground">{m.sso_unlink_last()}</p>
				{/if}
			{/if}
			{#if ssoState.enabled && identities.length === 0}
				<div>
					<Button disabled={busy} onclick={link}>{m.sso_link({ name: ssoState.name })}</Button>
				</div>
			{/if}
		</Card.Content>
	</Card.Root>
{/if}
