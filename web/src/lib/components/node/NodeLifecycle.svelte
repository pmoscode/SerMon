<script lang="ts">
	import PowerOff from '@lucide/svelte/icons/power-off';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import { timestampDate } from '@bufbuild/protobuf/wkt';
	import { api } from '#lib/api/client.js';
	import type { Node } from '#lib/api/gen/sermon/web/v1/node_pb.js';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { formatAgo } from '#lib/format/format.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { displayText } from '#lib/logs/text.js';
	import { notifyError } from '#lib/notify/notify.js';
	import { m } from '#lib/paraglide/messages.js';
	import { sessionState } from '#lib/stores/session.svelte.js';
	import { withStepUp } from '#lib/stores/stepup.svelte.js';

	/**
	 * Deactivating and deleting the node (M5-29, ADR 0041): the state for everyone, the actions for
	 * admins of its group, each with a step-up; deleting needs the host name typed.
	 */
	let { node, now, onchange }: { node: Node; now: Date; onchange: () => void } = $props();

	const id = $props.id();
	const locale = $derived(localeState.current);
	const allowed = $derived.by(() => {
		const groups = sessionState.current?.lifecycleGroupIds ?? [];
		return groups.includes('*') || groups.includes(node.groupId);
	});
	const deactivatedAt = $derived(
		node.deactivatedAt ? timestampDate(node.deactivatedAt) : undefined
	);
	const dueAt = $derived(node.deletionDueAt ? timestampDate(node.deletionDueAt) : undefined);
	const date = (d: Date) =>
		new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(d);

	let confirm = $state<'' | 'deactivate' | 'delete'>('');
	let typed = $state('');
	let busy = $state(false);
	let failure = $state('');
	let done = $state('');
	// The server compares the host name exactly; spaces at the edges don't count.
	const matches = $derived(typed.trim() === node.hostname);

	async function run(action: () => Promise<string>) {
		busy = true;
		failure = '';
		done = '';
		try {
			done = await withStepUp(action);
			confirm = '';
			typed = '';
			onchange();
		} catch (error) {
			failure = notifyError(error);
		} finally {
			busy = false;
		}
	}

	const deactivate = () =>
		run(async () => {
			await api.node.deactivateNode({ nodeId: node.id });
			return m.lifecycle_deactivated_done();
		});

	const remove = () =>
		run(async () => {
			const res = await api.node.deleteNode({ nodeId: node.id, confirmHostname: typed.trim() });
			const due = res.deletionDueAt ? timestampDate(res.deletionDueAt) : undefined;
			return m.lifecycle_deleted_done({ date: due ? date(due) : '–' });
		});

	const cancelDeletion = () =>
		run(async () => {
			await api.node.cancelNodeDeletion({ nodeId: node.id });
			return m.lifecycle_cancelled_done();
		});
</script>

<section class="flex flex-col gap-3 rounded-lg border bg-card p-4" aria-labelledby="{id}-title">
	<h2 id="{id}-title" class="flex items-center gap-1 text-lg font-medium">
		{m.lifecycle_title()}
		<HelpTip topic="concepts/node-lifecycle" />
	</h2>

	<dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
		<dt class="text-muted-foreground">{m.lifecycle_state()}</dt>
		<dd>
			<span role="status">
				{#if dueAt}
					<span class="inline-flex items-center gap-1 text-red-700 dark:text-red-400">
						<Trash2 class="size-4" aria-hidden="true" />
						{m.lifecycle_scheduled({ date: date(dueAt) })}
					</span>
				{:else if deactivatedAt}
					<span class="inline-flex items-center gap-1">
						<PowerOff class="size-4" aria-hidden="true" />
						{m.lifecycle_deactivated({ ago: formatAgo(deactivatedAt, now, locale) })}
					</span>
				{:else}
					{m.lifecycle_active()}
				{/if}
			</span>
		</dd>
	</dl>

	{#if allowed}
		<div class="flex flex-wrap gap-2">
			{#if !deactivatedAt}
				<Button
					variant="outline"
					size="sm"
					disabled={busy}
					onclick={() => (confirm = 'deactivate')}
				>
					<PowerOff aria-hidden="true" />
					{m.lifecycle_deactivate()}
				</Button>
			{/if}
			{#if dueAt}
				<Button variant="outline" size="sm" disabled={busy} onclick={cancelDeletion}>
					{m.lifecycle_cancel_deletion()}
				</Button>
			{:else}
				<Button variant="outline" size="sm" disabled={busy} onclick={() => (confirm = 'delete')}>
					<Trash2 aria-hidden="true" />
					{m.lifecycle_delete()}
				</Button>
			{/if}
		</div>

		{#if confirm}
			<div
				class="flex flex-col items-start gap-2 rounded-md border border-destructive/40 p-3 text-sm"
			>
				<p role="alert">
					{confirm === 'deactivate'
						? m.lifecycle_deactivate_warning()
						: m.lifecycle_delete_warning()}
				</p>
				{#if confirm === 'delete'}
					<label for="{id}-host" class="text-sm">
						{m.lifecycle_delete_label({ host: displayText(node.hostname) })}
					</label>
					<Input id="{id}-host" bind:value={typed} autocomplete="off" class="max-w-xs" />
				{/if}
				<div class="flex gap-2">
					<!-- Darker red than the variant: 4.5:1 on its tinted background (axe). -->
					<Button
						variant="destructive"
						size="sm"
						class="text-red-800 dark:text-red-300"
						disabled={busy || (confirm === 'delete' && !matches)}
						onclick={() => (confirm === 'deactivate' ? deactivate() : remove())}
					>
						{confirm === 'deactivate'
							? m.lifecycle_deactivate_confirm()
							: m.lifecycle_delete_confirm()}
					</Button>
					<Button
						variant="ghost"
						size="sm"
						onclick={() => {
							confirm = '';
							typed = '';
						}}>{m.lifecycle_cancel()}</Button
					>
				</div>
			</div>
		{/if}
	{/if}

	{#if done}
		<p class="text-sm" aria-live="polite">{done}</p>
	{/if}
	{#if failure}
		<p role="alert" class="text-sm text-destructive">{failure}</p>
	{/if}
</section>
