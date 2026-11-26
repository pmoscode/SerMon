<script lang="ts">
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import CircleCheck from '@lucide/svelte/icons/circle-check';
	import CircleDashed from '@lucide/svelte/icons/circle-dashed';
	import Lightbulb from '@lucide/svelte/icons/lightbulb';
	import { timestampDate } from '@bufbuild/protobuf/wkt';
	import { resolve } from '$app/paths';
	import { api } from '#lib/api/client.js';
	import { modulesApi } from '#lib/api/modules.js';
	import type { MetricDefinition } from '#lib/api/gen/sermon/web/v1/metrics_pb.js';
	import {
		ModuleState,
		type GetNodeModulesResponse,
		type ModuleHealth
	} from '#lib/api/gen/sermon/web/v1/module_pb.js';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import type { Snippet } from 'svelte';
	import { formatAgo, type Unit } from '#lib/format/format.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { displayText } from '#lib/logs/text.js';
	import { moduleLabel } from '#lib/modules/labels.js';
	import { enableQuery } from '#lib/modules/link.js';
	import { failing, modulesError, stateText } from '#lib/modules/text.js';
	import { m } from '#lib/paraglide/messages.js';
	import { helpState } from '#lib/stores/help.svelte.js';
	import { sessionState } from '#lib/stores/session.svelte.js';

	/**
	 * The modules of a node (ADR 0039, M5-07): what its group runs, how each does (from the agent's
	 * reports) with its metrics, and the agent's suggestions for modules the group doesn't run.
	 */
	let {
		nodeId,
		now,
		chart
	}: {
		nodeId: string;
		now: Date;
		/** A chart of one metric; the node page has the chart component loaded already. */
		chart?: Snippet<[metric: string, title: string, unit: Unit]>;
	} = $props();

	let data = $state.raw<GetNodeModulesResponse | undefined>(undefined);
	let metrics = $state.raw<MetricDefinition[]>([]);
	let status = $state<'loading' | 'ready' | 'error'>('loading');
	let failure = $state('');
	const locale = $derived(localeState.current);

	async function load() {
		try {
			const [node, catalog] = await Promise.all([
				modulesApi.modules.getNodeModules({ nodeId }),
				api.metrics.listMetrics({})
			]);
			data = node;
			metrics = catalog.metrics.filter((d) => d.module !== '');
			status = 'ready';
		} catch (error) {
			failure = modulesError(error);
			status = 'error';
		}
	}

	$effect(() => {
		void load();
	});

	const enabled = $derived((data?.settings ?? []).filter((s) => s.enabled));
	const open = $derived(
		(data?.hints ?? []).filter((h) => !enabled.some((s) => s.moduleId === h.moduleId))
	);
	const healthOf = (id: string): ModuleHealth | undefined =>
		data?.health.find((h) => h.moduleId === id);
	const title = (d: MetricDefinition) => helpState.get(`metrics/${d.name}`)?.title ?? d.name;
</script>

{#if status === 'loading'}
	<p role="status" class="text-muted-foreground">{m.loading()}</p>
{:else if status === 'error' || !data}
	<div class="flex flex-col items-start gap-2">
		<p role="alert" class="text-sm text-destructive">{failure}</p>
		<Button
			variant="outline"
			size="sm"
			onclick={() => {
				status = 'loading';
				void load();
			}}>{m.retry()}</Button
		>
	</div>
{:else}
	<div class="flex flex-col gap-6">
		<div class="flex items-center gap-2 text-sm text-muted-foreground">
			<span>{m.modules_intro()}</span>
			<HelpTip topic="concepts/modules" />
		</div>
		{#if data.vetoed}
			<p role="status" class="rounded-md border border-border p-3 text-sm">
				<TriangleAlert class="mr-1 inline size-4" aria-hidden="true" />{m.modules_node_vetoed()}
			</p>
		{:else if data.builtin.length === 0}
			<p class="text-sm text-muted-foreground">{m.modules_node_unknown()}</p>
		{/if}

		{#each open as hint (hint.moduleId)}
			<div
				class="flex flex-col gap-2 rounded-md border border-primary/40 bg-primary/5 p-3"
				data-testid="module-hint"
			>
				<p class="flex items-center gap-2 font-medium">
					<Lightbulb class="size-4" aria-hidden="true" />
					{m.modules_hint({ module: moduleLabel(hint.moduleId) })}
				</p>
				<p class="text-sm text-muted-foreground">
					{m.modules_hint_reasons({ reasons: hint.reasons.map(displayText).join(', ') })}
				</p>
				{#if sessionState.current?.orgAdmin}
					<Button
						class="self-start"
						size="sm"
						href={`${resolve('/(app)/admin')}?${enableQuery(data.groupId, hint.moduleId, hint.suggestedConfigJson)}`}
						>{m.modules_hint_enable()}</Button
					>
				{:else}
					<p class="text-sm">{m.modules_hint_ask_admin()}</p>
				{/if}
			</div>
		{/each}

		{#if enabled.length === 0}
			<p class="text-sm text-muted-foreground">{m.modules_node_none()}</p>
		{/if}
		{#each enabled as setting (setting.moduleId)}
			{@const health = healthOf(setting.moduleId)}
			{@const state = health?.state ?? ModuleState.UNSPECIFIED}
			{@const own = metrics.filter((d) => d.module === setting.moduleId)}
			<section class="flex flex-col gap-3" aria-labelledby="module-{setting.moduleId}">
				<div class="flex flex-wrap items-center gap-3">
					<h2 id="module-{setting.moduleId}" class="text-lg font-medium">
						{moduleLabel(setting.moduleId)}
					</h2>
					<span
						class="inline-flex items-center gap-1 text-sm {failing(state)
							? 'text-destructive'
							: 'text-muted-foreground'}"
						data-testid="module-state"
					>
						{#if state === ModuleState.OK}
							<CircleCheck class="size-4" aria-hidden="true" />
						{:else if failing(state)}
							<TriangleAlert class="size-4" aria-hidden="true" />
						{:else}
							<CircleDashed class="size-4" aria-hidden="true" />
						{/if}
						{stateText(state)}
					</span>
					{#if health}
						<span class="text-sm text-muted-foreground">
							{health.lastSuccess
								? m.modules_last_success({
										time: formatAgo(timestampDate(health.lastSuccess), now, locale)
									})
								: m.modules_never_succeeded()}
						</span>
					{/if}
				</div>
				{#if health?.reason}
					<p class="font-mono text-sm break-words">{displayText(health.reason)}</p>
				{/if}
				{#if own.length > 0 && chart}
					<div class="grid gap-6 lg:grid-cols-2">
						{#each own as d (d.name)}
							{@render chart(d.name, title(d), d.unit as Unit)}
						{/each}
					</div>
				{/if}
			</section>
		{/each}
	</div>
{/if}
