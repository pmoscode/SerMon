<script lang="ts">
	import EyeOff from '@lucide/svelte/icons/eye-off';
	import TriangleAlert from '@lucide/svelte/icons/triangle-exclamation-point';
	import { timestampFromMs } from '@bufbuild/protobuf/wkt';
	import { api } from '#lib/api/client.js';
	import type { NodeSummary } from '#lib/api/gen/sermon/web/v1/fleet_pb.js';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import { formatNumber } from '#lib/format/format.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { MAX_NODES } from '#lib/logs/filter.js';
	import { lostInRange } from '#lib/logs/notices.js';
	import { m } from '#lib/paraglide/messages.js';

	/**
	 * Why lines may be missing: nodes whose agent.toml forbids sending logs (allow_logs = false),
	 * and nodes whose agent lost journal entries in the range (ADR 0013).
	 */
	let { nodes, range }: { nodes: NodeSummary[]; range: { from: number; to: number } } = $props();

	const vetoed = $derived(nodes.filter((n) => n.logsVetoed).map((n) => n.hostname));
	let lost = $state<{ hostname: string; count: number }[]>([]);

	$effect(() => {
		const ids = nodes.slice(0, MAX_NODES).map((n) => n.nodeId);
		const hosts = new Map(nodes.map((n) => [n.nodeId, n.hostname]));
		const { from, to } = range;
		lost = [];
		if (ids.length === 0) return;
		const ctrl = new AbortController();
		api.metrics
			.queryMetrics(
				{
					selector: { metric: 'sermon_agent_journal_lost_entries', nodeIds: ids },
					from: timestampFromMs(from),
					to: timestampFromMs(to)
				},
				{ signal: ctrl.signal }
			)
			.then((res) => {
				lost = res.series
					.map((s) => ({ hostname: hosts.get(s.nodeId) ?? s.nodeId, count: lostInRange(s.points) }))
					.filter((x) => x.count > 0);
			})
			// Only a hint: without it the explorer works as before.
			.catch(() => {});
		return () => ctrl.abort();
	});
</script>

{#if vetoed.length > 0}
	<p class="flex items-start gap-2 rounded-md border border-border p-3 text-sm" role="note">
		<EyeOff class="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
		<span>{m.logs_notice_vetoed({ hosts: vetoed.join(', ') })}</span>
	</p>
{/if}
{#if lost.length > 0}
	<p class="flex items-start gap-2 rounded-md border border-amber-500/50 p-3 text-sm" role="note">
		<TriangleAlert
			class="mt-0.5 size-4 shrink-0 text-amber-700 dark:text-amber-400"
			aria-hidden="true"
		/>
		<span>
			{m.logs_notice_lost({
				hosts: lost
					.map((l) => `${l.hostname} (${formatNumber(l.count, localeState.current)})`)
					.join(', ')
			})}
			<HelpTip topic="metrics/sermon_agent_journal_lost_entries" />
		</span>
	</p>
{/if}
