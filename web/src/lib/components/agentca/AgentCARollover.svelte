<script lang="ts">
	import CircleCheck from '@lucide/svelte/icons/circle-check';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import { timestampDate } from '@bufbuild/protobuf/wkt';
	import type { Timestamp } from '@bufbuild/protobuf/wkt';
	import { adminError } from '#lib/admin/admin.js';
	import { rolloverState } from '#lib/agentca/rollover.js';
	import { agentCAApi } from '#lib/api/agentca.js';
	import type { AgentCA } from '#lib/api/gen/sermon/web/v1/agent_ca_pb.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { formatNumber } from '#lib/format/format.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { m } from '#lib/paraglide/messages.js';
	import { withStepUp } from '#lib/stores/stepup.svelte.js';

	/**
	 * The agent CA for superadmins (V1-19): when the first trusted CA expires, the CAs by role, and
	 * the rollover – start it, then retire the previous CA once it has no certificates left – each
	 * with a step-up; the server checks everything again and writes the instance audit log.
	 */
	let cas = $state<AgentCA[]>([]);
	let now = $state(new Date());
	let status = $state<'loading' | 'ready'>('loading');
	let failure = $state('');
	let notice = $state('');
	let busy = $state(false);
	let confirming = $state<'start' | 'retire' | undefined>();
	const locale = $derived(localeState.current);
	const view = $derived(rolloverState(cas, now));
	const sorted = $derived([...cas].sort((a, b) => b.id - a.id));

	async function load() {
		failure = '';
		try {
			cas = (await agentCAApi.getAgentCAStatus({})).authorities;
			now = new Date();
		} catch (error) {
			failure = adminError(error);
		} finally {
			status = 'ready';
		}
	}

	$effect(() => {
		void load();
	});

	function date(t: Timestamp | Date | undefined): string {
		if (!t) return '–';
		return (t instanceof Date ? t : timestampDate(t)).toLocaleDateString(locale);
	}

	function role(ca: AgentCA): string {
		if (ca.retireTime) return m.agentca_role_retired({ date: date(ca.retireTime) });
		return ca.issuing ? m.agentca_role_issuing() : m.agentca_role_previous();
	}

	async function run(action: 'start' | 'retire') {
		failure = '';
		notice = '';
		busy = true;
		let refused = '';
		try {
			if (action === 'start') {
				await withStepUp(() => agentCAApi.startAgentCARollover({}));
				notice = m.agentca_done_start();
			} else {
				const id = view.previous?.id ?? 0;
				await withStepUp(() => agentCAApi.retireAgentCA({ id }));
				notice = m.agentca_done_retire();
			}
			confirming = undefined;
		} catch (error) {
			refused = adminError(
				error,
				action === 'start' ? m.agentca_error_in_progress() : m.agentca_error_in_use()
			);
		} finally {
			busy = false;
		}
		await load();
		if (refused) failure = refused;
	}
</script>

{#if status === 'loading'}
	<p role="status" class="text-muted-foreground">{m.loading()}</p>
{:else}
	<div class="flex flex-col gap-6">
		<p class="max-w-prose text-sm text-muted-foreground">{m.agentca_intro()}</p>
		{#if failure}
			<p role="alert" class="rounded-md border border-destructive/50 p-3 text-sm text-destructive">
				{failure}
			</p>
		{/if}
		{#if notice}
			<p role="status" class="rounded-md border p-3 text-sm">{notice}</p>
		{/if}

		{#if view.expires}
			{@const params = { date: date(view.expires), days: String(view.daysLeft ?? 0) }}
			<p
				data-level={view.level}
				class="flex max-w-prose items-start gap-2 rounded-md border p-3 text-sm {view.level ===
				'critical'
					? 'border-red-300 bg-red-50 text-red-800 dark:bg-red-950 dark:text-red-200'
					: view.level === 'warning'
						? 'border-amber-300 bg-amber-50 text-amber-900 dark:bg-amber-950 dark:text-amber-200'
						: ''}"
			>
				{#if view.level === 'ok'}
					<CircleCheck class="mt-0.5 size-4 shrink-0" aria-hidden="true" />
				{:else}
					<TriangleAlert class="mt-0.5 size-4 shrink-0" aria-hidden="true" />
				{/if}
				<span>
					{#if view.expired}
						{m.agentca_expired(params)}
					{:else if view.level === 'ok'}
						{m.agentca_expires(params)}
					{:else}
						{m.agentca_expires_soon(params)}
					{/if}
				</span>
			</p>
		{/if}

		<section class="flex max-w-prose flex-col gap-3" aria-labelledby="agentca-rollover">
			<h2 id="agentca-rollover" class="text-lg font-medium">{m.agentca_rollover()}</h2>
			{#if view.previous}
				<p class="text-sm">{m.agentca_running({ date: date(view.issuing?.createTime) })}</p>
				<p class="text-sm">
					{view.canRetire
						? m.agentca_retire_ready()
						: m.agentca_retire_wait({
								count: formatNumber(Number(view.previous.certificatesInUse), locale),
								date: date(view.replacedBy)
							})}
				</p>
			{:else}
				<p class="text-sm">{m.agentca_idle()}</p>
			{/if}
			{#if confirming}
				<div class="flex flex-col gap-3 rounded-lg border p-4">
					<p class="text-sm">
						{confirming === 'start' ? m.agentca_explain_start() : m.agentca_explain_retire()}
					</p>
					<div class="flex gap-2">
						<Button type="button" disabled={busy} onclick={() => run(confirming!)}>
							{confirming === 'start' ? m.agentca_action_start() : m.agentca_action_retire()}
						</Button>
						<Button type="button" variant="ghost" onclick={() => (confirming = undefined)}>
							{m.stepup_cancel()}
						</Button>
					</div>
				</div>
			{:else if view.canStart}
				<div>
					<Button type="button" disabled={busy} onclick={() => (confirming = 'start')}>
						{m.agentca_action_start()}
					</Button>
				</div>
			{:else if view.previous}
				<div>
					<Button
						type="button"
						variant="outline"
						disabled={busy || !view.canRetire}
						onclick={() => (confirming = 'retire')}
					>
						{m.agentca_action_retire()}
					</Button>
				</div>
			{/if}
		</section>

		<div class="overflow-x-auto rounded-lg border">
			<table class="w-full text-sm">
				<caption class="sr-only">{m.agentca_table()}</caption>
				<thead class="bg-muted/50 text-left">
					<tr>
						<th scope="col" class="px-3 py-2 font-medium">{m.agentca_col_role()}</th>
						<th scope="col" class="px-3 py-2 font-medium">{m.agentca_col_created()}</th>
						<th scope="col" class="px-3 py-2 font-medium">{m.agentca_col_expires()}</th>
						<th scope="col" class="px-3 py-2 text-right font-medium">{m.agentca_col_in_use()}</th>
						<th scope="col" class="px-3 py-2 font-medium">{m.agentca_col_fingerprint()}</th>
					</tr>
				</thead>
				<tbody class="divide-y">
					{#each sorted as ca (ca.id)}
						<tr class:text-muted-foreground={!!ca.retireTime}>
							<th scope="row" class="px-3 py-2 text-left font-medium">{role(ca)}</th>
							<td class="px-3 py-2 whitespace-nowrap">{date(ca.createTime)}</td>
							<td class="px-3 py-2 whitespace-nowrap">{date(ca.expireTime)}</td>
							<td class="px-3 py-2 text-right tabular-nums">
								{formatNumber(Number(ca.certificatesInUse), locale)}
							</td>
							<td class="px-3 py-2 font-mono text-xs break-all">{ca.rootFingerprint}</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	</div>
{/if}
