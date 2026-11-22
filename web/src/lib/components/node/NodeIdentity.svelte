<script lang="ts">
	import KeyRound from '@lucide/svelte/icons/key-round';
	import ShieldAlert from '@lucide/svelte/icons/shield-alert';
	import ShieldCheck from '@lucide/svelte/icons/shield-check';
	import { timestampDate } from '@bufbuild/protobuf/wkt';
	import { api } from '#lib/api/client.js';
	import type { Node } from '#lib/api/gen/sermon/web/v1/node_pb.js';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import { formatAgo, formatDuration } from '#lib/format/format.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { notifyError } from '#lib/notify/notify.js';
	import { m } from '#lib/paraglide/messages.js';
	import { sessionState } from '#lib/stores/session.svelte.js';
	import { withStepUp } from '#lib/stores/stepup.svelte.js';

	/**
	 * The node's identity (M2-23): lock, last address and takeover window for everyone; rotating,
	 * revoking, unlocking and allowing a takeover for operators of its group, each with a step-up.
	 * Lock reasons and addresses come from the server and stay text.
	 */
	let { node, now, onchange }: { node: Node; now: Date; onchange: () => void } = $props();

	const locale = $derived(localeState.current);
	const allowed = $derived.by(() => {
		const groups = sessionState.current?.nodeIdentityGroupIds ?? [];
		return groups.includes('*') || groups.includes(node.groupId);
	});
	const lockedAt = $derived(node.lockedAt ? timestampDate(node.lockedAt) : undefined);
	const takeoverUntil = $derived.by(() => {
		const until = node.takeoverUntil ? timestampDate(node.takeoverUntil) : undefined;
		return until && until > now ? until : undefined;
	});

	let confirm = $state<'' | 'unlock' | 'revoke'>('');
	let busy = $state(false);
	let failure = $state('');
	let done = $state('');

	async function run(action: () => Promise<string>) {
		busy = true;
		failure = '';
		done = '';
		try {
			done = await withStepUp(action);
			confirm = '';
			onchange();
		} catch (error) {
			failure = notifyError(error);
		} finally {
			busy = false;
		}
	}

	function rotate(revokeOnly: boolean) {
		return run(async () => {
			const res = await api.node.rotateNodeKeys({
				target: { case: 'nodes', value: { ids: [node.id] } },
				revokeOnly
			});
			if (revokeOnly) return m.identity_revoked();
			return res.online > 0 ? m.identity_rotated_online() : m.identity_rotated_offline();
		});
	}

	function unlock() {
		return run(async () => {
			await api.node.unlockNode({ nodeId: node.id });
			return m.identity_unlocked();
		});
	}

	function allowTakeover() {
		return run(async () => {
			await api.node.allowNodeTakeover({ nodeId: node.id });
			return m.identity_takeover_done();
		});
	}
</script>

<section class="flex flex-col gap-3 rounded-lg border bg-card p-4" aria-labelledby="identity-title">
	<h2 id="identity-title" class="flex items-center gap-1 text-lg font-medium">
		{m.identity_title()}
		<HelpTip topic="concepts/node-identity" />
	</h2>

	<dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
		<dt class="text-muted-foreground">{m.identity_state()}</dt>
		<dd>
			<span role="status">
				{#if lockedAt}
					<span class="inline-flex items-center gap-1 text-red-700 dark:text-red-400">
						<ShieldAlert class="size-4" aria-hidden="true" />
						{m.identity_locked({ ago: formatAgo(lockedAt, now, locale) })}
					</span>
					{#if node.lockReason}
						<span class="text-muted-foreground">– {node.lockReason}</span>
					{/if}
				{:else}
					<span class="inline-flex items-center gap-1">
						<ShieldCheck class="size-4" aria-hidden="true" />
						{m.identity_not_locked()}
					</span>
				{/if}
			</span>
		</dd>
		{#if node.lastIp}
			<dt class="text-muted-foreground">{m.identity_last_ip()}</dt>
			<dd class="font-mono">{node.lastIp}</dd>
		{/if}
		{#if takeoverUntil}
			<dt class="text-muted-foreground">{m.identity_takeover()}</dt>
			<dd>
				{m.identity_takeover_open({
					remaining: formatDuration((takeoverUntil.getTime() - now.getTime()) / 1000, locale)
				})}
			</dd>
		{/if}
	</dl>

	{#if allowed}
		<div class="flex flex-wrap gap-2">
			<Button variant="outline" size="sm" disabled={busy} onclick={() => rotate(false)}>
				<KeyRound aria-hidden="true" />
				{m.identity_rotate()}
			</Button>
			<Button variant="outline" size="sm" disabled={busy} onclick={() => (confirm = 'revoke')}>
				{m.identity_revoke()}
			</Button>
			{#if lockedAt}
				<Button variant="outline" size="sm" disabled={busy} onclick={() => (confirm = 'unlock')}>
					{m.identity_unlock()}
				</Button>
			{/if}
			{#if !takeoverUntil}
				<Button variant="outline" size="sm" disabled={busy} onclick={allowTakeover}>
					{m.identity_allow_takeover()}
				</Button>
			{/if}
		</div>

		{#if confirm}
			<div
				class="flex flex-col items-start gap-2 rounded-md border border-destructive/40 p-3 text-sm"
			>
				<p role="alert">
					{confirm === 'unlock' ? m.identity_unlock_warning() : m.identity_revoke_warning()}
				</p>
				<div class="flex gap-2">
					<Button
						variant="destructive"
						size="sm"
						disabled={busy}
						onclick={() => (confirm === 'unlock' ? unlock() : rotate(true))}
					>
						{confirm === 'unlock' ? m.identity_unlock_confirm() : m.identity_revoke_confirm()}
					</Button>
					<Button variant="ghost" size="sm" onclick={() => (confirm = '')}
						>{m.identity_cancel()}</Button
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
