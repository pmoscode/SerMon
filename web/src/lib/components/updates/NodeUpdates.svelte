<script lang="ts">
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import Power from '@lucide/svelte/icons/power';
	import PackageCheck from '@lucide/svelte/icons/package-check';
	import { timestampDate } from '@bufbuild/protobuf/wkt';
	import { codeOf, Code } from '#lib/api/client.js';
	import {
		UpdateAvailability,
		UpdateScope,
		UpdateStatus,
		type GetNodeUpdatesResponse
	} from '#lib/api/gen/sermon/web/v1/update_pb.js';
	import { updatesApi } from '#lib/api/updates.js';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { formatAgo } from '#lib/format/format.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { displayText } from '#lib/logs/text.js';
	import { notifyError } from '#lib/notify/notify.js';
	import { m } from '#lib/paraglide/messages.js';
	import { sessionState } from '#lib/stores/session.svelte.js';
	import { withStepUp } from '#lib/stores/stepup.svelte.js';
	import PackageTable from './PackageTable.svelte';

	/**
	 * The Updates tab of a node (M4-14): its last report with the packages; "update now" and
	 * "reboot" for operators of its group, each confirmed – the update with the package list – and
	 * with a step-up. What the host forbids (helper.toml) is said, not hidden.
	 */
	let { nodeId, groupId, now }: { nodeId: string; groupId: string; now: Date } = $props();

	const locale = $derived(localeState.current);
	const allowedIn = (groups: string[] | undefined) =>
		(groups ?? []).includes('*') || (groups ?? []).includes(groupId);
	const mayUpdate = $derived(allowedIn(sessionState.current?.runUpdatesGroupIds));
	const mayReboot = $derived(allowedIn(sessionState.current?.rebootGroupIds));

	let data = $state<GetNodeUpdatesResponse>();
	let status = $state<'loading' | 'ready' | 'error'>('loading');
	let confirm = $state<'' | 'update' | 'reboot'>('');
	let scope = $state<'security' | 'all'>('security');
	let reboot = $state(false);
	let reason = $state('');
	let busy = $state(false);
	let failure = $state('');
	let done = $state('');

	async function load() {
		try {
			data = await updatesApi.updates.getNodeUpdates({ nodeId });
			status = 'ready';
		} catch {
			status = 'error';
		}
	}

	$effect(() => {
		void load();
	});

	const node = $derived(data?.node);
	const packages = $derived(data?.packages ?? []);
	const listed = $derived(node?.availability === UpdateAvailability.LISTED);
	const permissions = $derived(node?.permissions);
	const security = $derived(packages.filter((p) => p.security));
	const chosen = $derived(scope === 'security' ? security : packages);
	const collectedAt = $derived(node?.collectedAt ? timestampDate(node.collectedAt) : undefined);

	const statusText = $derived.by(() => {
		switch (node?.status) {
			case UpdateStatus.SECURITY:
				return m.updates_status_security({ count: node.securityCount, total: node.totalCount });
			case UpdateStatus.UPDATES:
				return m.updates_status_updates({ total: node.totalCount });
			case UpdateStatus.CURRENT:
				return m.updates_status_current();
			case UpdateStatus.UNAVAILABLE:
				return m.updates_status_unavailable();
			default:
				return m.updates_status_unknown();
		}
	});

	async function act(action: () => Promise<string>) {
		busy = true;
		failure = '';
		done = '';
		try {
			done = await withStepUp(action);
			confirm = '';
		} catch (error) {
			failure = notifyError(error);
		} finally {
			busy = false;
		}
	}

	function startUpdate() {
		return act(async () => {
			await updatesApi.updates.startUpdates({
				nodeIds: [nodeId],
				scope: scope === 'security' ? UpdateScope.SECURITY : UpdateScope.ALL,
				reboot
			});
			return m.updates_started();
		});
	}

	function startReboot() {
		return act(async () => {
			await updatesApi.updates.rebootNodes({ nodeIds: [nodeId], reason: reason.trim() });
			return m.updates_reboot_started();
		});
	}

	async function refresh() {
		busy = true;
		failure = '';
		done = '';
		try {
			await updatesApi.updates.refreshUpdateInfo({ nodeId });
			done = m.updates_refresh_requested();
		} catch (error) {
			failure =
				codeOf(error) === Code.ResourceExhausted
					? m.updates_refresh_too_soon()
					: notifyError(error);
		} finally {
			busy = false;
		}
	}

	function openUpdate() {
		scope = security.length > 0 ? 'security' : 'all';
		reboot = false;
		confirm = 'update';
	}
</script>

<section class="flex flex-col gap-4" aria-labelledby="updates-title">
	<h2 id="updates-title" class="flex items-center gap-1 text-lg font-medium">
		{m.updates_title()}
		<HelpTip topic="concepts/updates" />
	</h2>

	{#if status === 'loading'}
		<p role="status" class="text-muted-foreground">{m.loading()}</p>
	{:else if status === 'error' || !node}
		<div class="flex flex-col items-start gap-2">
			<p role="alert" class="text-destructive">{m.error_unavailable()}</p>
			<Button variant="outline" size="sm" onclick={load}>{m.retry()}</Button>
		</div>
	{:else}
		<dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
			<dt class="text-muted-foreground">{m.updates_state()}</dt>
			<dd role="status">{statusText}</dd>
			{#if node.reason}
				<dt class="text-muted-foreground">{m.updates_reason()}</dt>
				<dd>{displayText(node.reason)}</dd>
			{/if}
			{#if node.rebootRequired}
				<dt class="text-muted-foreground">{m.updates_reboot()}</dt>
				<dd>
					{m.updates_reboot_required()}
					{#if node.rebootReason}<span class="text-muted-foreground">
							– {displayText(node.rebootReason)}</span
						>{/if}
				</dd>
			{/if}
			{#if collectedAt}
				<dt class="text-muted-foreground">{m.updates_collected()}</dt>
				<dd>{formatAgo(collectedAt, now, locale)}</dd>
			{/if}
			{#if listed && !permissions}
				<dt class="text-muted-foreground">{m.updates_helper()}</dt>
				<dd>{m.updates_no_helper()}</dd>
			{/if}
		</dl>

		{#if permissions && !permissions.remoteUpdates}
			<p class="text-sm" role="note">{m.updates_veto_updates()}</p>
		{/if}

		{#if mayUpdate || mayReboot}
			<div class="flex flex-wrap gap-2">
				{#if mayUpdate}
					<Button
						size="sm"
						disabled={busy || !listed || packages.length === 0 || !permissions?.remoteUpdates}
						onclick={openUpdate}
					>
						<PackageCheck aria-hidden="true" />
						{m.updates_update_now()}
					</Button>
					<Button variant="outline" size="sm" disabled={busy} onclick={refresh}>
						<RefreshCw aria-hidden="true" />
						{m.updates_refresh()}
					</Button>
				{/if}
				{#if mayReboot}
					<Button
						variant="outline"
						size="sm"
						disabled={busy || !permissions?.remoteReboot}
						onclick={() => (confirm = 'reboot')}
					>
						<Power aria-hidden="true" />
						{m.updates_reboot_now()}
					</Button>
				{/if}
			</div>
			{#if mayReboot && permissions && !permissions.remoteReboot}
				<p class="text-sm text-muted-foreground" role="note">{m.updates_veto_reboot()}</p>
			{/if}
		{/if}

		{#if confirm === 'update'}
			<div
				class="flex flex-col gap-3 rounded-md border border-destructive/40 p-3 text-sm"
				role="group"
				aria-labelledby="update-confirm"
			>
				<p id="update-confirm" class="font-medium">{m.updates_confirm_title()}</p>
				<fieldset class="flex flex-wrap gap-4">
					<legend class="sr-only">{m.updates_scope()}</legend>
					<label class="flex items-center gap-1">
						<input
							type="radio"
							bind:group={scope}
							value="security"
							disabled={security.length === 0}
						/>
						{m.updates_scope_security({ count: security.length })}
					</label>
					<label class="flex items-center gap-1">
						<input type="radio" bind:group={scope} value="all" />
						{m.updates_scope_all({ count: packages.length })}
					</label>
				</fieldset>
				<label class="flex items-center gap-1">
					<input
						type="checkbox"
						bind:checked={reboot}
						disabled={!mayReboot || !permissions?.remoteReboot}
					/>
					{m.updates_with_reboot()}
				</label>
				{#if node.packagesOmitted}
					<p>{m.updates_packages_omitted({ total: node.totalCount })}</p>
				{/if}
				<PackageTable packages={chosen} caption={m.updates_confirm_title()} />
				<div class="flex gap-2">
					<Button
						variant="destructive"
						size="sm"
						disabled={busy || chosen.length === 0}
						onclick={startUpdate}
					>
						{m.updates_confirm({ count: chosen.length })}
					</Button>
					<Button variant="ghost" size="sm" onclick={() => (confirm = '')}
						>{m.identity_cancel()}</Button
					>
				</div>
			</div>
		{:else if confirm === 'reboot'}
			<div
				class="flex flex-col items-start gap-3 rounded-md border border-destructive/40 p-3 text-sm"
				role="group"
				aria-labelledby="reboot-confirm"
			>
				<p id="reboot-confirm" class="font-medium">{m.updates_reboot_confirm_title()}</p>
				<div class="flex w-full max-w-md flex-col gap-1">
					<Label for="reboot-reason">{m.updates_reboot_reason()}</Label>
					<Input id="reboot-reason" bind:value={reason} maxlength={200} />
				</div>
				<div class="flex gap-2">
					<Button
						variant="destructive"
						size="sm"
						disabled={busy || reason.trim() === ''}
						onclick={startReboot}
					>
						{m.updates_reboot_confirm()}
					</Button>
					<Button variant="ghost" size="sm" onclick={() => (confirm = '')}
						>{m.identity_cancel()}</Button
					>
				</div>
			</div>
		{/if}

		{#if packages.length > 0 && confirm !== 'update'}
			<PackageTable {packages} caption={m.updates_packages()} />
		{/if}

		{#if done}
			<p class="text-sm" aria-live="polite">{done}</p>
		{/if}
		{#if failure}
			<p role="alert" class="text-sm text-destructive">{failure}</p>
		{/if}
	{/if}
</section>
