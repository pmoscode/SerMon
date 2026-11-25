<script lang="ts">
	import ArrowLeft from '@lucide/svelte/icons/arrow-left';
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import MetricChart from '#lib/charts/MetricChart.svelte';
	import NodeFacts from '#lib/components/node/NodeFacts.svelte';
	import NodeTags from '#lib/components/node/NodeTags.svelte';
	import SecuritySourceNotice from '#lib/components/node/SecuritySourceNotice.svelte';
	import TimeRangePicker from '#lib/components/time/TimeRangePicker.svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Tabs from '#lib/components/ui/tabs/index.js';
	import { parseNodeView } from '#lib/alerts/links.js';
	import { emptyFilter, filterParams, type LogFilter } from '#lib/logs/filter.js';
	import {
		emptySecurityFilter,
		securityFilterParams,
		type SecurityFilter
	} from '#lib/security/filter.js';
	import { NodeState } from '#lib/node/node.svelte.js';
	import { m } from '#lib/paraglide/messages.js';
	import { timeRange } from '#lib/stores/timerange.svelte.js';

	/** Charts follow the time range; in live mode it moves on every 10 s. */
	const LIVE_INTERVAL = 10_000;

	const nodeState = new NodeState(page.params.id ?? '');
	// A link from an alert (M3-19) names the range of the charts and the area to open.
	const linked = parseNodeView(page.url.searchParams);
	if (linked.range) timeRange.zoom(linked.range.from, linked.range.to);
	let bits = $state(false);
	let tab = $state<string>(linked.tab ?? 'cpu');
	let logFilter = $state<LogFilter>(emptyFilter());
	let securityFilter = $state<SecurityFilter>(emptySecurityFilter());

	$effect(() => nodeState.start());
	$effect(() => {
		const timer = setInterval(() => timeRange.refresh(), LIVE_INTERVAL);
		return () => clearInterval(timer);
	});

	const id = $derived(nodeState.id);
	const now = $derived(new Date(nodeState.updatedAt));
	const notIdle = (labels: Record<string, string>) => labels.mode === 'idle';
	const onlySome = { kind: 'some' };
</script>

<svelte:head>
	<title>{nodeState.node?.hostname ?? m.node_title()} · SerMon</title>
</svelte:head>

<Button href={resolve('/(app)/fleet')} variant="ghost" size="sm" class="mb-3">
	<ArrowLeft aria-hidden="true" />
	{m.node_back()}
</Button>

{#if nodeState.status === 'loading'}
	<p role="status" class="text-muted-foreground">{m.loading()}</p>
{:else if nodeState.status === 'not_found'}
	<section class="flex flex-col items-start gap-2" aria-labelledby="node-missing">
		<h1 id="node-missing" class="text-2xl font-semibold tracking-tight">{m.node_not_found()}</h1>
		<p class="text-sm text-muted-foreground">{m.node_not_found_text()}</p>
	</section>
{:else if nodeState.status === 'error' || !nodeState.node}
	<div class="flex flex-col items-start gap-3">
		<p role="alert" class="text-destructive">{m.error_unavailable()}</p>
		<Button variant="outline" onclick={() => nodeState.load()}>{m.retry()}</Button>
	</div>
{:else}
	<div class="flex flex-col gap-6">
		<NodeFacts node={nodeState.node} {now} />
		<SecuritySourceNotice node={nodeState.node} />
		<NodeTags node={nodeState.node} onchange={() => void nodeState.load()} />

		<div class="flex flex-wrap items-center justify-between gap-2">
			<TimeRangePicker />
		</div>

		<Tabs.Root bind:value={tab}>
			<Tabs.List aria-label={m.node_areas()}>
				<Tabs.Trigger value="cpu">{m.node_tab_cpu()}</Tabs.Trigger>
				<Tabs.Trigger value="memory">{m.node_tab_memory()}</Tabs.Trigger>
				<Tabs.Trigger value="disk">{m.node_tab_disk()}</Tabs.Trigger>
				<Tabs.Trigger value="network">{m.node_tab_network()}</Tabs.Trigger>
				<Tabs.Trigger value="processes">{m.node_tab_processes()}</Tabs.Trigger>
				<Tabs.Trigger value="logs">{m.node_tab_logs()}</Tabs.Trigger>
				<Tabs.Trigger value="security">{m.node_tab_security()}</Tabs.Trigger>
				<Tabs.Trigger value="updates">{m.node_tab_updates()}</Tabs.Trigger>
				<Tabs.Trigger value="identity">{m.node_tab_identity()}</Tabs.Trigger>
			</Tabs.List>

			<Tabs.Content value="cpu" class="grid gap-6 pt-4 lg:grid-cols-2">
				<MetricChart
					nodeId={id}
					title={m.chart_cpu_modes()}
					unit="ratio_ppm"
					queries={[{ metric: 'sermon_cpu_utilization_ratio', skip: notIdle }]}
				/>
				<MetricChart
					nodeId={id}
					title={m.chart_load()}
					unit="load"
					queries={[{ metric: 'sermon_load_average_per_cpu' }]}
				/>
				<MetricChart
					nodeId={id}
					title={m.chart_pressure()}
					unit="ratio_ppm"
					queries={[{ metric: 'sermon_pressure_stall_ratio', labels: onlySome }]}
				/>
				<MetricChart
					nodeId={id}
					title={m.chart_processes()}
					unit="count"
					queries={[
						{ metric: 'sermon_processes_running', label: m.chart_running() },
						{ metric: 'sermon_processes_blocked', label: m.chart_blocked() }
					]}
				/>
			</Tabs.Content>

			<Tabs.Content value="memory" class="grid gap-6 pt-4 lg:grid-cols-2">
				<MetricChart
					nodeId={id}
					title={m.chart_memory()}
					unit="bytes"
					queries={[
						{ metric: 'sermon_memory_used_bytes', label: m.chart_used() },
						{ metric: 'sermon_memory_cached_bytes', label: m.chart_cached() },
						{ metric: 'sermon_memory_buffers_bytes', label: m.chart_buffers() },
						{ metric: 'sermon_memory_available_bytes', label: m.chart_available() }
					]}
				/>
				<MetricChart
					nodeId={id}
					title={m.chart_swap()}
					unit="bytes"
					queries={[{ metric: 'sermon_swap_used_bytes', label: m.chart_used() }]}
				/>
				<MetricChart
					nodeId={id}
					title={m.chart_faults()}
					unit="per_second"
					queries={[{ metric: 'sermon_memory_major_faults_per_second' }]}
				/>
				<MetricChart
					nodeId={id}
					title={m.chart_oom()}
					unit="per_second"
					queries={[{ metric: 'sermon_memory_oom_kills_per_second' }]}
				/>
			</Tabs.Content>

			<Tabs.Content value="disk" class="flex flex-col gap-6 pt-4">
				<section aria-labelledby="fs-title" class="flex flex-col gap-2">
					<h2 id="fs-title" class="text-lg font-medium">{m.node_filesystems()}</h2>
					{#if nodeState.filesystems.length === 0}
						<p class="text-sm text-muted-foreground">{m.node_fs_none()}</p>
					{:else}
						<!-- Loaded on demand: the tab's table is not part of the page's first load (budget). -->
						{#await import('#lib/components/node/FilesystemTable.svelte')}
							<p role="status" class="text-sm text-muted-foreground">{m.loading()}</p>
						{:then { default: FilesystemTable }}
							<FilesystemTable filesystems={nodeState.filesystems} {now} />
						{/await}
					{/if}
				</section>
				<div class="grid gap-6 lg:grid-cols-2">
					<MetricChart
						nodeId={id}
						title={m.chart_disk_throughput()}
						unit="bytes_per_second"
						queries={[
							{ metric: 'sermon_disk_read_bytes_per_second', label: m.chart_read() },
							{ metric: 'sermon_disk_write_bytes_per_second', label: m.chart_write() }
						]}
					/>
					<MetricChart
						nodeId={id}
						title={m.chart_disk_iops()}
						unit="per_second"
						queries={[
							{ metric: 'sermon_disk_reads_per_second', label: m.chart_read() },
							{ metric: 'sermon_disk_writes_per_second', label: m.chart_write() }
						]}
					/>
					<MetricChart
						nodeId={id}
						title={m.chart_disk_latency()}
						unit="seconds"
						queries={[{ metric: 'sermon_disk_await_seconds' }]}
					/>
					<MetricChart
						nodeId={id}
						title={m.chart_disk_busy()}
						unit="ratio_ppm"
						queries={[{ metric: 'sermon_disk_busy_ratio' }]}
					/>
				</div>
			</Tabs.Content>

			<Tabs.Content value="network" class="flex flex-col gap-4 pt-4">
				<label class="flex items-center gap-2 self-end text-sm">
					<input type="checkbox" bind:checked={bits} class="size-4 accent-primary" />
					{m.chart_bits()}
				</label>
				<div class="grid gap-6 lg:grid-cols-2">
					<MetricChart
						nodeId={id}
						title={m.chart_network()}
						unit="bytes_per_second"
						{bits}
						queries={[
							{ metric: 'sermon_network_receive_bytes_per_second', label: '↓' },
							{ metric: 'sermon_network_transmit_bytes_per_second', label: '↑' }
						]}
					/>
					<MetricChart
						nodeId={id}
						title={m.chart_packets()}
						unit="per_second"
						queries={[
							{ metric: 'sermon_network_receive_packets_per_second', label: '↓' },
							{ metric: 'sermon_network_transmit_packets_per_second', label: '↑' }
						]}
					/>
					<MetricChart
						nodeId={id}
						title={m.chart_network_errors()}
						unit="per_second"
						queries={[
							{ metric: 'sermon_network_receive_errors_per_second', label: m.chart_errors_in() },
							{ metric: 'sermon_network_transmit_errors_per_second', label: m.chart_errors_out() },
							{ metric: 'sermon_network_receive_drops_per_second', label: m.chart_drops_in() },
							{ metric: 'sermon_network_transmit_drops_per_second', label: m.chart_drops_out() }
						]}
					/>
					<MetricChart
						nodeId={id}
						title={m.chart_tcp()}
						unit="count"
						queries={[
							{ metric: 'sermon_tcp_established', label: 'ESTABLISHED' },
							{ metric: 'sermon_tcp_time_wait', label: 'TIME-WAIT' }
						]}
					/>
					<MetricChart
						nodeId={id}
						title={m.chart_retransmits()}
						unit="per_second"
						queries={[{ metric: 'sermon_tcp_retransmits_per_second' }]}
					/>
					<MetricChart
						nodeId={id}
						title={m.chart_conntrack()}
						unit="ratio_ppm"
						queries={[{ metric: 'sermon_conntrack_utilization_ratio' }]}
					/>
				</div>
			</Tabs.Content>
			<!-- Loaded and streamed only while open: the agent sends processes only on demand (KONZEPT §9). -->
			<Tabs.Content value="processes" class="pt-4">
				{#if tab === 'processes'}
					{#await import('#lib/components/processes/ProcessView.svelte')}
						<p role="status" class="text-muted-foreground">{m.loading()}</p>
					{:then { default: ProcessView }}
						<ProcessView nodeId={id} />
					{/await}
				{/if}
			</Tabs.Content>
			<!-- Loaded on demand: keeps the first load of the page within its budget (KONZEPT §17). -->
			<Tabs.Content value="logs" class="flex flex-col gap-3 pt-4">
				{#if tab === 'logs'}
					<Button
						href={`${resolve('/(app)/logs')}?${filterParams({ ...logFilter, nodes: [id] })}`}
						variant="link"
						size="sm"
						class="self-end px-0"
					>
						{m.node_logs_open()}
					</Button>
					{#await import('#lib/components/logs/LogExplorer.svelte')}
						<p role="status" class="text-muted-foreground">{m.loading()}</p>
					{:then { default: LogExplorer }}
						<LogExplorer filter={logFilter} onFilter={(f) => (logFilter = f)} fixedNode={id} />
					{/await}
				{/if}
			</Tabs.Content>
			<Tabs.Content value="security" class="flex flex-col gap-3 pt-4">
				{#if tab === 'security'}
					<Button
						href={`${resolve('/(app)/security')}?${securityFilterParams({ ...securityFilter, nodes: [id] })}`}
						variant="link"
						size="sm"
						class="self-end px-0"
					>
						{m.node_security_open()}
					</Button>
					{#await import('#lib/components/securityevents/SecurityDashboard.svelte')}
						<p role="status" class="text-muted-foreground">{m.loading()}</p>
					{:then { default: SecurityDashboard }}
						<SecurityDashboard
							filter={securityFilter}
							onFilter={(f) => (securityFilter = f)}
							fixedNode={id}
						/>
					{/await}
				{/if}
			</Tabs.Content>
			<Tabs.Content value="updates" class="flex flex-col gap-6 pt-4">
				<!-- The derived series of the updates (M4-16), written by the server with the metrics. -->
				<MetricChart
					nodeId={id}
					title={m.chart_updates()}
					unit="count"
					height={140}
					queries={[
						{ metric: 'sermon_updates_pending', label: m.chart_updates_all() },
						{ metric: 'sermon_updates_security_pending', label: m.chart_updates_security() }
					]}
				/>
				{#if tab === 'updates'}
					{#await import('#lib/components/updates/NodeUpdates.svelte')}
						<p role="status" class="text-muted-foreground">{m.loading()}</p>
					{:then { default: NodeUpdates }}
						<NodeUpdates nodeId={id} groupId={nodeState.node.groupId} {now} />
					{/await}
				{/if}
			</Tabs.Content>

			<Tabs.Content value="identity" class="pt-4">
				{#if tab === 'identity'}
					{#await import('#lib/components/node/NodeIdentity.svelte')}
						<p role="status" class="text-muted-foreground">{m.loading()}</p>
					{:then { default: NodeIdentity }}
						<NodeIdentity node={nodeState.node} {now} onchange={() => void nodeState.load()} />
					{/await}
				{/if}
			</Tabs.Content>
		</Tabs.Root>
	</div>
{/if}
