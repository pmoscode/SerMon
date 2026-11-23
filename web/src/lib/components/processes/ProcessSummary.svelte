<script lang="ts">
	import { timestampDate } from '@bufbuild/protobuf/wkt';
	import type { ProcessSnapshot } from '#lib/api/gen/sermon/web/v1/live_view_pb.js';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import { formatBytes, formatDuration, formatNumber, formatPercent } from '#lib/format/format.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { m } from '#lib/paraglide/messages.js';

	/** The header of the live view, like top's: CPU, memory, swap, load, uptime and tasks. */
	let { snapshot }: { snapshot: ProcessSnapshot } = $props();

	const locale = $derived(localeState.current);
	const s = $derived(snapshot.summary);
	const tasks = $derived(s?.tasks);
	const load = $derived(
		[s?.load1, s?.load5, s?.load15]
			.map((v) =>
				v === undefined
					? m.procs_na()
					: new Intl.NumberFormat(locale, {
							minimumFractionDigits: 2,
							maximumFractionDigits: 2
						}).format(v)
			)
			.join(' · ')
	);
	const memoryUsed = $derived(s ? Number(s.memoryTotalBytes) - Number(s.memoryAvailableBytes) : 0);
	const swapUsed = $derived(s ? Number(s.swapTotalBytes) - Number(s.swapFreeBytes) : 0);
	const bytes = (n: number) => formatBytes(Math.max(0, n), locale);
	const count = (n: number | undefined) => formatNumber(n ?? 0, locale);
</script>

<section aria-labelledby="procs-summary" class="flex flex-col gap-2 rounded-lg border bg-card p-3">
	<div class="flex flex-wrap items-center gap-2">
		<h2 id="procs-summary" class="text-base font-medium">{m.procs_summary()}</h2>
		<HelpTip topic="concepts/live-view" />
		{#if snapshot.takenAt}
			<span class="ml-auto text-xs text-muted-foreground">
				{m.procs_taken_at({
					time: timestampDate(snapshot.takenAt).toLocaleTimeString(locale)
				})}
			</span>
		{/if}
	</div>
	<dl class="grid grid-cols-2 gap-x-6 gap-y-2 text-sm sm:grid-cols-3 lg:grid-cols-6">
		<div>
			<dt class="flex items-center gap-0.5 text-xs text-muted-foreground">
				{m.procs_cpu()}<HelpTip topic="figures/cpu_busy" />
			</dt>
			<dd class="font-mono tabular-nums">
				{s?.cpuBusyRatio === undefined ? '–' : formatPercent(s.cpuBusyRatio, locale)}
				<span class="text-xs text-muted-foreground"
					>{m.procs_cpus({ count: count(s?.cpuCount) })}</span
				>
			</dd>
		</div>
		<div>
			<dt class="flex items-center gap-0.5 text-xs text-muted-foreground">
				{m.procs_memory()}<HelpTip topic="figures/memory_used" />
			</dt>
			<dd class="font-mono tabular-nums">
				{m.procs_of({ used: bytes(memoryUsed), total: bytes(Number(s?.memoryTotalBytes ?? 0)) })}
			</dd>
		</div>
		<div>
			<dt class="text-xs text-muted-foreground">{m.procs_swap()}</dt>
			<dd class="font-mono tabular-nums">
				{!s || s.swapTotalBytes === 0n
					? m.procs_swap_none()
					: m.procs_of({ used: bytes(swapUsed), total: bytes(Number(s.swapTotalBytes)) })}
			</dd>
		</div>
		<div>
			<dt class="text-xs text-muted-foreground">{m.procs_load()}</dt>
			<dd class="font-mono tabular-nums">{load}</dd>
		</div>
		<div>
			<dt class="flex items-center gap-0.5 text-xs text-muted-foreground">
				{m.procs_uptime()}<HelpTip topic="figures/uptime" />
			</dt>
			<dd>
				{s?.uptimeSeconds === undefined
					? m.procs_na()
					: formatDuration(Number(s.uptimeSeconds), locale)}
			</dd>
		</div>
		<div class="col-span-2 sm:col-span-3 lg:col-span-6">
			<dt class="text-xs text-muted-foreground">{m.procs_tasks()}</dt>
			<dd class="tabular-nums">
				{m.procs_tasks_value({
					total: count(tasks?.total),
					running: count(tasks?.running),
					sleeping: count(tasks?.sleeping),
					stopped: count(tasks?.stopped),
					zombie: count(tasks?.zombie)
				})}
			</dd>
		</div>
	</dl>
</section>
