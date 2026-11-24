<script lang="ts">
	import { durationMs } from '@bufbuild/protobuf/wkt';
	import type { Duration } from '@bufbuild/protobuf/wkt';
	import { Code, codeOf } from '#lib/api/client.js';
	import type { GetAlertStatisticsResponse } from '#lib/api/gen/sermon/web/v1/alert_pb.js';
	import { alertingApi } from '#lib/api/alerting.js';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import { formatDuration, formatNumber } from '#lib/format/format.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { m } from '#lib/paraglide/messages.js';

	/**
	 * The alert statistics (KONZEPT §16, M3-19): the loudest rules and the mean times to
	 * acknowledgement (MTTA) and resolution (MTTR) of the last 7 or 30 days.
	 */
	let days = $state<7 | 30>(7);
	let data = $state.raw<GetAlertStatisticsResponse | undefined>(undefined);
	let status = $state<'loading' | 'ready' | 'error'>('loading');
	let failure = $state('');

	async function load(d: number) {
		status = 'loading';
		try {
			data = await alertingApi.alerts.getAlertStatistics({ days: d });
			status = 'ready';
		} catch (error) {
			if (codeOf(error) === Code.Unauthenticated) return;
			failure =
				codeOf(error) === Code.PermissionDenied ? m.alertstats_forbidden() : m.error_unavailable();
			status = 'error';
		}
	}

	$effect(() => {
		void load(days);
	});

	const locale = $derived(localeState.current);
	const n = (v: number | undefined) => formatNumber(v ?? 0, locale);
	const time = (d: Duration | undefined) =>
		d === undefined ? '–' : formatDuration(durationMs(d) / 1000, locale);
	const name = (r: { ruleName?: { de: string; en: string }; ruleId: string }) =>
		(locale === 'de' ? r.ruleName?.de : r.ruleName?.en) || r.ruleId;
	const totals = $derived(data?.totals);
</script>

<div class="flex flex-col gap-4">
	<div class="flex flex-wrap items-center gap-2">
		<div role="group" aria-label={m.range_label()} class="inline-flex gap-1">
			{#each [7, 30] as const as d (d)}
				<Button
					variant={days === d ? 'secondary' : 'ghost'}
					size="sm"
					aria-pressed={days === d}
					onclick={() => (days = d)}
				>
					{d === 7 ? m.range_7d() : m.range_30d()}
				</Button>
			{/each}
		</div>
		<HelpTip topic="concepts/alert-statistics" />
	</div>

	{#if status === 'loading' && !data}
		<p role="status" class="text-sm text-muted-foreground">{m.loading()}</p>
	{:else if status === 'error'}
		<div class="flex flex-col items-start gap-2">
			<p role="alert" class="text-sm text-destructive">{failure}</p>
			<Button variant="outline" size="sm" onclick={() => load(days)}>{m.retry()}</Button>
		</div>
	{:else if data}
		<dl class="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-5">
			<div class="rounded-lg border bg-card p-3">
				<dt class="flex items-center gap-0.5 text-xs text-muted-foreground">
					{m.alertstats_firings()}<HelpTip topic="figures/alert_firings" />
				</dt>
				<dd class="text-2xl font-semibold tabular-nums">{n(totals?.firings)}</dd>
			</div>
			<div class="rounded-lg border bg-card p-3">
				<dt class="flex items-center gap-0.5 text-xs text-muted-foreground">
					{m.alertstats_mtta()}<HelpTip topic="figures/mtta" />
				</dt>
				<dd class="text-2xl font-semibold tabular-nums">{time(totals?.mtta)}</dd>
			</div>
			<div class="rounded-lg border bg-card p-3">
				<dt class="flex items-center gap-0.5 text-xs text-muted-foreground">
					{m.alertstats_mttr()}<HelpTip topic="figures/mttr" />
				</dt>
				<dd class="text-2xl font-semibold tabular-nums">{time(totals?.mttr)}</dd>
			</div>
			<div class="rounded-lg border bg-card p-3">
				<dt class="text-xs text-muted-foreground">{m.alertstats_open()}</dt>
				<dd class="text-2xl font-semibold tabular-nums">{n(totals?.open)}</dd>
			</div>
			<div class="rounded-lg border bg-card p-3">
				<dt class="text-xs text-muted-foreground">{m.alertstats_unacknowledged()}</dt>
				<dd class="text-2xl font-semibold tabular-nums">{n(totals?.unacknowledged)}</dd>
			</div>
		</dl>
		{#if data.truncated}
			<p class="text-sm text-amber-700 dark:text-amber-400">{m.alertstats_truncated()}</p>
		{/if}

		<section class="flex flex-col gap-2" aria-labelledby="alertstats-rules">
			<h2 id="alertstats-rules" class="text-lg font-medium">{m.alertstats_loudest()}</h2>
			{#if data.rules.length === 0}
				<p class="rounded-md border border-dashed border-border p-4 text-sm">
					{m.alertstats_empty()}
				</p>
			{:else}
				<div class="overflow-x-auto rounded-md border border-border">
					<table class="w-full min-w-[40rem] text-sm">
						<thead class="bg-muted text-left text-xs">
							<tr>
								<th scope="col" class="px-2 py-1.5 font-medium">{m.alertstats_col_rule()}</th>
								<th scope="col" class="px-2 py-1.5 text-right font-medium"
									>{m.alertstats_firings()}</th
								>
								<th scope="col" class="px-2 py-1.5 text-right font-medium"
									>{m.alertstats_col_nodes()}</th
								>
								<th scope="col" class="px-2 py-1.5 text-right font-medium">{m.alertstats_mtta()}</th
								>
								<th scope="col" class="px-2 py-1.5 text-right font-medium">{m.alertstats_mttr()}</th
								>
								<th scope="col" class="px-2 py-1.5 text-right font-medium">{m.alertstats_open()}</th
								>
								<th scope="col" class="px-2 py-1.5 text-right font-medium"
									>{m.alertstats_col_flapping()}</th
								>
							</tr>
						</thead>
						<tbody class="tabular-nums">
							{#each data.rules as r (r.ruleId)}
								<tr class="border-t border-border">
									<th scope="row" class="px-2 py-1.5 text-left font-normal">
										{name(r)}
										<span class="block font-mono text-xs text-muted-foreground">{r.ruleId}</span>
									</th>
									<td class="px-2 py-1.5 text-right font-medium">{n(r.figures?.firings)}</td>
									<td class="px-2 py-1.5 text-right">{n(r.nodes)}</td>
									<td class="px-2 py-1.5 text-right">{time(r.figures?.mtta)}</td>
									<td class="px-2 py-1.5 text-right">{time(r.figures?.mttr)}</td>
									<td class="px-2 py-1.5 text-right">{n(r.figures?.open)}</td>
									<td class="px-2 py-1.5 text-right">{n(r.figures?.flapping)}</td>
								</tr>
							{/each}
						</tbody>
					</table>
				</div>
			{/if}
		</section>
	{/if}
</div>
