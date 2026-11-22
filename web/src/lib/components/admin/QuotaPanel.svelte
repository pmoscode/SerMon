<script lang="ts">
	import type { GetOrganizationQuotasResponse } from '#lib/api/gen/sermon/web/v1/access_pb.js';
	import { adminApi } from '#lib/api/admin.js';
	import { adminError } from '#lib/admin/admin.js';
	import { formatNumber } from '#lib/format/format.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { QUOTA_FIELDS, quotaLabel, type QuotaKey } from '#lib/organizations/quotas.js';
	import { m } from '#lib/paraglide/messages.js';

	/**
	 * The quotas the superadmin set for this organization and how much of them is used (M2-32):
	 * nodes and members (with open invitations) count; the others are limits per node.
	 */
	let usage = $state<GetOrganizationQuotasResponse | undefined>();
	let failure = $state('');
	const locale = $derived(localeState.current);

	$effect(() => {
		adminApi.access
			.getOrganizationQuotas({})
			.then((res) => (usage = res))
			.catch((error: unknown) => (failure = adminError(error)));
	});

	function used(key: QuotaKey, s: GetOrganizationQuotasResponse): number | undefined {
		if (key === 'maxNodes') return Number(s.nodes);
		if (key === 'maxMembers') return Number(s.members + s.openInvitations);
		return undefined;
	}

	function text(key: QuotaKey, s: GetOrganizationQuotasResponse): string {
		const limit = s.quotas?.[key];
		const count = used(key, s);
		const n = (v: number) => formatNumber(v, locale);
		if (count !== undefined) {
			return limit === undefined
				? m.admin_quota_used_free({ used: n(count) })
				: m.admin_quota_used({ used: n(count), limit: n(limit) });
		}
		return limit === undefined ? m.admin_quota_default() : m.admin_quota_value({ limit: n(limit) });
	}
</script>

<div class="flex flex-col gap-3">
	<p class="text-sm text-muted-foreground">{m.admin_quotas_intro()}</p>
	{#if failure}
		<p role="alert" class="rounded-md border border-destructive/50 p-3 text-sm text-destructive">
			{failure}
		</p>
	{:else if !usage}
		<p role="status" class="text-muted-foreground">{m.loading()}</p>
	{:else}
		<dl class="grid max-w-xl grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-sm">
			{#each QUOTA_FIELDS as field (field.key)}
				<dt class="text-muted-foreground">{quotaLabel(field.key)}</dt>
				<dd class="tabular-nums">{text(field.key, usage)}</dd>
			{/each}
		</dl>
	{/if}
</div>
