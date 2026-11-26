<script lang="ts">
	import { timestampDate } from '@bufbuild/protobuf/wkt';
	import { RolloutState, type Rollout } from '#lib/api/gen/sermon/web/v1/update_pb.js';
	import { updatesApi } from '#lib/api/updates.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { displayText } from '#lib/logs/text.js';
	import { m } from '#lib/paraglide/messages.js';
	import { sessionState } from '#lib/stores/session.svelte.js';
	import { withStepUp } from '#lib/stores/stepup.svelte.js';
	import { actionError, allowedIn } from '#lib/updates/actions.js';
	import { rolloutLabel, rolloutNodeLabel } from '#lib/updates/labels.js';

	/**
	 * The latest rollouts (M4-15); a running one can be cancelled with run_updates on its group,
	 * after a confirmation and a step-up (M4-31).
	 */
	let { rollouts, onchange }: { rollouts: Rollout[]; onchange: () => Promise<void> } = $props();

	let confirm = $state('');
	let busy = $state(false);
	let failure = $state<{ rolloutId: string; text: string }>();

	const dateFormat = $derived(
		new Intl.DateTimeFormat(localeState.current, {
			weekday: 'short',
			day: 'numeric',
			month: 'short',
			hour: '2-digit',
			minute: '2-digit'
		})
	);

	const mayCancel = (r: Rollout) =>
		r.state === RolloutState.RUNNING &&
		allowedIn(sessionState.current?.runUpdatesGroupIds, r.groupId);

	async function cancel(r: Rollout) {
		busy = true;
		failure = undefined;
		try {
			await withStepUp(() => updatesApi.updates.cancelRollout({ rolloutId: r.rolloutId }));
			confirm = '';
		} catch (error) {
			const text = actionError(error, m.rollout_cancel_ended());
			if (text) {
				failure = { rolloutId: r.rolloutId, text };
				confirm = '';
			}
		} finally {
			busy = false;
		}
		await onchange();
	}
</script>

{#if rollouts.length === 0}
	<p class="text-sm text-muted-foreground">{m.updatespage_rollouts_none()}</p>
{:else}
	<ul class="divide-y rounded-md border text-sm">
		{#each rollouts as r (r.rolloutId)}
			<li class="flex flex-col gap-1 px-3 py-2">
				<div class="flex flex-wrap items-center gap-x-3">
					<span class="font-medium">{r.groupName}</span>
					<span>{r.manual ? m.updatespage_manual() : m.updatespage_scheduled()}</span>
					<span>{rolloutLabel(r.state)}</span>
					{#if r.windowStart}<span class="text-muted-foreground"
							>{dateFormat.format(timestampDate(r.windowStart))}</span
						>{/if}
					{#if mayCancel(r) && confirm !== r.rolloutId}
						<Button
							variant="ghost"
							size="sm"
							class="ml-auto"
							disabled={busy}
							onclick={() => {
								confirm = r.rolloutId;
								failure = undefined;
							}}>{m.rollout_cancel()}</Button
						>
					{/if}
				</div>
				{#if r.reason}<p class="text-red-700 dark:text-red-400">
						{displayText(r.reason)}
					</p>{/if}
				{#if r.nodes.length > 0}
					<p class="text-muted-foreground">
						{#each r.nodes as n, i (n.nodeId)}{i > 0 ? ' · ' : ''}{displayText(n.hostname)}: {rolloutNodeLabel(
								n.state
							)}{n.reason ? ` (${displayText(n.reason)})` : ''}{/each}
					</p>
				{/if}
				{#if confirm === r.rolloutId && mayCancel(r)}
					<div
						class="flex flex-col items-start gap-2 rounded-md border border-destructive/40 p-3"
						role="group"
						aria-labelledby="rollout-cancel-{r.rolloutId}"
					>
						<p id="rollout-cancel-{r.rolloutId}" class="font-medium">
							{m.rollout_cancel_title()}
						</p>
						<p>{m.rollout_cancel_text()}</p>
						<div class="flex gap-2">
							<Button variant="destructive" size="sm" disabled={busy} onclick={() => cancel(r)}
								>{m.rollout_cancel_confirm()}</Button
							>
							<Button variant="ghost" size="sm" onclick={() => (confirm = '')}
								>{m.rollout_cancel_keep()}</Button
							>
						</div>
					</div>
				{/if}
				{#if failure?.rolloutId === r.rolloutId}
					<p role="alert" class="text-destructive">{failure.text}</p>
				{/if}
			</li>
		{/each}
	</ul>
{/if}
