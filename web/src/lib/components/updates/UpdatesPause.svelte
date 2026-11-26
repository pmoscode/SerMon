<script lang="ts">
	import CirclePause from '@lucide/svelte/icons/circle-pause';
	import { timestampDate, type Timestamp } from '@bufbuild/protobuf/wkt';
	import { updatesApi } from '#lib/api/updates.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { notifyError } from '#lib/notify/notify.js';
	import { m } from '#lib/paraglide/messages.js';
	import { sessionState } from '#lib/stores/session.svelte.js';
	import { withStepUp } from '#lib/stores/stepup.svelte.js';

	/**
	 * Whether the organization's updates are paused (M4-31), for everyone on the page; org admins
	 * pause (after a confirmation) and resume them, each with a step-up.
	 */
	let { pausedSince, onchange }: { pausedSince?: Timestamp; onchange: () => Promise<void> } =
		$props();

	let confirm = $state(false);
	let busy = $state(false);
	let failure = $state('');

	const admin = $derived(sessionState.current?.orgAdmin ?? false);
	const since = $derived(
		pausedSince
			? new Intl.DateTimeFormat(localeState.current, {
					dateStyle: 'medium',
					timeStyle: 'short'
				}).format(timestampDate(pausedSince))
			: ''
	);

	async function set(paused: boolean) {
		busy = true;
		failure = '';
		try {
			await withStepUp(() => updatesApi.policies.setUpdatesPaused({ paused }));
			confirm = false;
			await onchange();
		} catch (error) {
			failure = notifyError(error);
		} finally {
			busy = false;
		}
	}
</script>

{#if pausedSince}
	<div
		class="flex flex-wrap items-center gap-3 rounded-md border border-amber-500/50 bg-amber-500/10 p-3 text-sm"
		role="status"
	>
		<CirclePause class="size-5 shrink-0 text-amber-700 dark:text-amber-400" aria-hidden="true" />
		<div class="flex flex-col gap-1">
			<p class="font-medium">{m.updates_paused_since({ time: since })}</p>
			<p class="text-muted-foreground">{m.updates_paused_text()}</p>
		</div>
		{#if admin}
			<Button variant="outline" size="sm" class="ml-auto" disabled={busy} onclick={() => set(false)}
				>{m.updates_resume()}</Button
			>
		{/if}
	</div>
{:else if admin}
	{#if confirm}
		<div
			class="flex flex-col items-start gap-2 rounded-md border border-destructive/40 p-3 text-sm"
			role="group"
			aria-labelledby="pause-confirm"
		>
			<p id="pause-confirm" class="font-medium">{m.updates_pause_title()}</p>
			<p>{m.updates_pause_text()}</p>
			<div class="flex gap-2">
				<Button variant="destructive" size="sm" disabled={busy} onclick={() => set(true)}
					>{m.updates_pause()}</Button
				>
				<Button variant="ghost" size="sm" onclick={() => (confirm = false)}
					>{m.identity_cancel()}</Button
				>
			</div>
		</div>
	{:else}
		<div>
			<Button variant="outline" size="sm" onclick={() => (confirm = true)}>
				<CirclePause aria-hidden="true" />
				{m.updates_pause()}
			</Button>
		</div>
	{/if}
{/if}
{#if failure}
	<p role="alert" class="text-sm text-destructive">{failure}</p>
{/if}
