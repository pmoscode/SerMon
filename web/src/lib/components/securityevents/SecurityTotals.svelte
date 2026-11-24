<script lang="ts">
	import type { SecurityEventTotals } from '#lib/api/gen/sermon/web/v1/security_event_pb.js';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import { formatNumber } from '#lib/format/format.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { m } from '#lib/paraglide/messages.js';
	import { failedKinds, sudoFailureKinds, type SecurityFilter } from '#lib/security/filter.js';

	/**
	 * The totals of the range; each one filters the table to its events (KONZEPT §16.4).
	 */
	let {
		totals,
		filter,
		onFilter
	}: {
		totals: SecurityEventTotals | undefined;
		filter: SecurityFilter;
		onFilter: (filter: SecurityFilter) => void;
	} = $props();

	const locale = $derived(localeState.current);
	const n = (v: bigint | undefined) => formatNumber(Number(v ?? 0n), locale);
	const base = $derived({ ...filter, kinds: [], user: undefined, newOnly: false });

	const tiles = $derived([
		{
			label: m.secdash_total_failed(),
			value: n(totals?.failedLogins),
			topic: 'figures/failed_logins',
			action: m.secdash_show_failed(),
			apply: () => onFilter({ ...base, kinds: [...failedKinds] })
		},
		{
			label: m.secdash_total_logins(),
			value: n(totals?.logins),
			sub: m.secdash_total_new({ count: n(totals?.newSourceLogins) }),
			topic: 'figures/new_source_logins',
			action: m.secdash_show_new(),
			apply: () => onFilter({ ...base, kinds: ['ssh.login.success'], newOnly: true })
		},
		{
			label: m.secdash_total_root(),
			value: n(totals?.rootLogins),
			topic: 'figures/root_logins',
			action: m.secdash_show_root(),
			apply: () => onFilter({ ...base, kinds: ['ssh.login.success'], user: 'root' })
		},
		{
			label: m.secdash_total_sudo(),
			value: n(totals?.sudoFailures),
			topic: 'figures/sudo_failures',
			action: m.secdash_show_sudo(),
			apply: () => onFilter({ ...base, kinds: [...sudoFailureKinds] })
		}
	]);
</script>

<dl class="grid grid-cols-2 gap-3 lg:grid-cols-4">
	{#each tiles as t (t.topic)}
		<div class="flex flex-col gap-1 rounded-lg border bg-card p-3">
			<dt class="flex items-center gap-0.5 text-xs text-muted-foreground">
				{t.label}<HelpTip topic={t.topic} />
			</dt>
			<dd class="flex flex-col items-start gap-1">
				<span class="text-2xl font-semibold tabular-nums">{t.value}</span>
				{#if t.sub}<span class="text-xs text-muted-foreground tabular-nums">{t.sub}</span>{/if}
				<button
					type="button"
					class="text-xs text-primary underline-offset-2 hover:underline focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
					onclick={t.apply}>{t.action}</button
				>
			</dd>
		</div>
	{/each}
</dl>
