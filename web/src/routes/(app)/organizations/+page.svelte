<script lang="ts">
	import Pencil from '@lucide/svelte/icons/pencil';
	import ShieldBan from '@lucide/svelte/icons/shield-ban';
	import { timestampDate } from '@bufbuild/protobuf/wkt';
	import {
		type OrganizationSummary,
		OrganizationStatus
	} from '#lib/api/gen/sermon/web/v1/organization_pb.js';
	import { adminApi } from '#lib/api/admin.js';
	import { adminError } from '#lib/admin/admin.js';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import CreateOrganization from '#lib/components/organizations/CreateOrganization.svelte';
	import OrganizationEditor from '#lib/components/organizations/OrganizationEditor.svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import { formatBytes, formatNumber } from '#lib/format/format.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { QUOTA_FIELDS, quotaLabel } from '#lib/organizations/quotas.js';
	import { m } from '#lib/paraglide/messages.js';
	import { sessionState } from '#lib/stores/session.svelte.js';

	/**
	 * The organizations of the instance for superadmins (M2-32, ADR 0029): counts, approximate
	 * storage, status and quotas – no tenant data. Create (first admin by invitation) and edit
	 * name and quotas, each with a step-up. Lock, schedule the deletion, unlock, delete and export
	 * the audit log (M2-33), each with the typed name and a step-up.
	 */
	let organizations = $state<OrganizationSummary[]>([]);
	let status = $state<'loading' | 'ready'>('loading');
	let failure = $state('');
	let editing = $state<string | undefined>();
	let lifecycle = $state<string | undefined>();
	let notice = $state('');
	let saved = $state('');
	const locale = $derived(localeState.current);

	async function load() {
		failure = '';
		try {
			organizations = (await adminApi.organization.listOrganizations({})).organizations;
		} catch (error) {
			failure = adminError(error);
		} finally {
			status = 'ready';
		}
	}

	$effect(() => {
		if (sessionState.current?.superadmin) void load();
	});

	function quotasText(o: OrganizationSummary): string {
		const set = QUOTA_FIELDS.filter((f) => o.quotas?.[f.key] !== undefined).map(
			(f) => `${quotaLabel(f.key)}: ${formatNumber(o.quotas![f.key]!, locale)}`
		);
		return set.length ? set.join(' · ') : m.orgs_quotas_none();
	}

	function dateOf(t: OrganizationSummary['lockTime']): string {
		return t ? timestampDate(t).toLocaleDateString(locale) : '–';
	}

	function statusText(o: OrganizationSummary): string {
		switch (o.status) {
			case OrganizationStatus.ACTIVE:
				return m.orgs_status_active();
			case OrganizationStatus.LOCKED:
				return m.orgs_status_locked({ date: dateOf(o.lockTime) });
			case OrganizationStatus.DELETION_SCHEDULED:
				return m.orgs_status_scheduled({ date: dateOf(o.deletionTime) });
			default:
				return '–';
		}
	}

	async function changed(text: string) {
		lifecycle = undefined;
		notice = text;
		await load();
	}

	async function done(org: OrganizationSummary, ok: boolean) {
		editing = undefined;
		if (!ok) return;
		saved = org.id;
		await load();
	}
</script>

<svelte:head>
	<title>{m.orgs_title()} · SerMon</title>
</svelte:head>

<div class="mb-6 flex items-center gap-2">
	<h1 class="text-2xl font-semibold tracking-tight">{m.orgs_title()}</h1>
	<HelpTip topic="pages/organizations" />
</div>

{#if !sessionState.current?.superadmin}
	<p role="alert" class="text-sm">{m.orgs_forbidden()}</p>
{:else if status === 'loading'}
	<p role="status" class="text-muted-foreground">{m.loading()}</p>
{:else}
	<div class="flex flex-col gap-8">
		{#if failure}
			<p role="alert" class="rounded-md border border-destructive/50 p-3 text-sm text-destructive">
				{failure}
			</p>
		{/if}
		{#if notice}
			<p role="status" class="rounded-md border p-3 text-sm">{notice}</p>
		{/if}
		{#if organizations.length === 0}
			<p class="text-sm text-muted-foreground">{m.orgs_empty()}</p>
		{:else}
			<div class="overflow-x-auto rounded-lg border">
				<table class="w-full text-sm">
					<thead class="bg-muted/50 text-left">
						<tr>
							<th scope="col" class="px-3 py-2 font-medium">{m.orgs_col_name()}</th>
							<th scope="col" class="px-3 py-2 font-medium">{m.orgs_col_created()}</th>
							<th scope="col" class="px-3 py-2 text-right font-medium">{m.orgs_col_members()}</th>
							<th scope="col" class="px-3 py-2 text-right font-medium">{m.orgs_col_nodes()}</th>
							<th scope="col" class="px-3 py-2 text-right font-medium">{m.orgs_col_storage()}</th>
							<th scope="col" class="px-3 py-2 font-medium">{m.orgs_col_status()}</th>
							<th scope="col" class="px-3 py-2 font-medium">{m.orgs_col_quotas()}</th>
							<th scope="col" class="px-3 py-2"
								><span class="sr-only">{m.orgs_col_actions()}</span></th
							>
						</tr>
					</thead>
					<tbody class="divide-y">
						{#each organizations as o (o.id)}
							<tr>
								<th scope="row" class="px-3 py-2 text-left font-medium">{o.name}</th>
								<td class="px-3 py-2 whitespace-nowrap">
									{o.createTime ? timestampDate(o.createTime).toLocaleDateString(locale) : '–'}
								</td>
								<td class="px-3 py-2 text-right tabular-nums"
									>{formatNumber(Number(o.memberCount), locale)}</td
								>
								<td class="px-3 py-2 text-right tabular-nums"
									>{formatNumber(Number(o.nodeCount), locale)}</td
								>
								<td class="px-3 py-2 text-right whitespace-nowrap tabular-nums">
									{formatBytes(Number(o.storageBytes), locale)}
								</td>
								<td
									class="px-3 py-2"
									class:text-destructive={o.status !== OrganizationStatus.ACTIVE}
									>{statusText(o)}</td
								>
								<td class="px-3 py-2 text-muted-foreground">{quotasText(o)}</td>
								<td class="px-3 py-2 text-right whitespace-nowrap">
									<Button
										size="sm"
										variant="ghost"
										aria-label={m.orgs_edit_label({ name: o.name })}
										onclick={() => {
											saved = '';
											notice = '';
											lifecycle = undefined;
											editing = o.id;
										}}
									>
										<Pencil aria-hidden="true" />
										{m.orgs_edit()}
									</Button>
									<Button
										size="sm"
										variant="ghost"
										aria-label={m.orgs_lifecycle_label({ name: o.name })}
										aria-expanded={lifecycle === o.id}
										onclick={() => {
											saved = '';
											notice = '';
											editing = undefined;
											lifecycle = lifecycle === o.id ? undefined : o.id;
										}}
									>
										<ShieldBan aria-hidden="true" />
										{m.orgs_lifecycle()}
									</Button>
								</td>
							</tr>
							{#if editing === o.id}
								<tr>
									<td colspan="8" class="p-3">
										<OrganizationEditor organization={o} onDone={(ok) => done(o, ok)} />
									</td>
								</tr>
							{:else if lifecycle === o.id}
								<tr>
									<td colspan="8" class="p-3">
										{#await import('#lib/components/organizations/OrganizationLifecycle.svelte') then { default: OrganizationLifecycle }}
											<OrganizationLifecycle organization={o} onDone={changed} />
										{/await}
									</td>
								</tr>
							{:else if saved === o.id}
								<tr>
									<td colspan="8" class="px-3 py-2 text-sm"
										><p role="status">{m.orgs_saved()}</p></td
									>
								</tr>
							{/if}
						{/each}
					</tbody>
				</table>
			</div>
		{/if}
		<CreateOrganization onCreated={load} />
		{#await import('#lib/components/organizations/DeleteAccount.svelte') then { default: DeleteAccount }}
			<DeleteAccount />
		{/await}
	</div>
{/if}
