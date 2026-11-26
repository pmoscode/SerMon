<script lang="ts">
	import RotateCcw from '@lucide/svelte/icons/rotate-ccw';
	import Search from '@lucide/svelte/icons/search';
	import ZoomOut from '@lucide/svelte/icons/zoom-out';
	import type { NodeSummary } from '#lib/api/gen/sermon/web/v1/fleet_pb.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import {
		cutBytes,
		emptyFilter,
		logPresets,
		MAX_TEXT_BYTES,
		MAX_UNITS,
		severities,
		type LogFilter,
		type LogPreset,
		type SeverityName,
		type SourceName
	} from '#lib/logs/filter.js';
	import { presetLabels, severityNameLabel, sourceLabel } from '#lib/logs/labels.js';
	import { m } from '#lib/paraglide/messages.js';

	/**
	 * The filters of the log explorer. Ranges apply at once; the other fields apply with "Search"
	 * (or Enter), so typing doesn't start a query per key.
	 */
	let {
		filter,
		nodes,
		fixedNode = false,
		onApply
	}: {
		filter: LogFilter;
		/** The visible nodes, for the node and group choice. */
		nodes: NodeSummary[];
		/** On a node page: no node and group choice. */
		fixedNode?: boolean;
		onApply: (filter: LogFilter) => void;
	} = $props();

	const select =
		'h-9 rounded-md border border-input bg-background px-2 text-sm focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none';

	let node = $state('');
	let group = $state('');
	let source = $state<'' | SourceName>('');
	let units = $state('');
	let severity = $state<'' | SeverityName>('');
	let text = $state('');
	let ignoreCase = $state(false);

	// The form follows the applied filter (URL, zoom, links).
	$effect.pre(() => {
		node = filter.nodes.length === 1 ? filter.nodes[0] : filter.nodes.length > 1 ? 'many' : '';
		group = filter.group ?? '';
		source = filter.sources.length === 1 ? filter.sources[0] : '';
		units = filter.units.join(', ');
		severity = filter.severity ?? '';
		text = filter.text;
		ignoreCase = filter.ignoreCase;
	});

	const groups = $derived(
		[...new Map(nodes.map((n) => [n.groupId, n.groupName])).entries()].sort((a, b) =>
			a[1].localeCompare(b[1], localeState.current)
		)
	);
	const absolute = $derived(filter.from !== undefined && filter.to !== undefined);
	const dateTime = (ms: number) =>
		new Intl.DateTimeFormat(localeState.current, {
			dateStyle: 'short',
			timeStyle: 'medium'
		}).format(ms);

	function current(): LogFilter {
		return {
			...filter,
			nodes: node === 'many' ? filter.nodes : node ? [node] : [],
			group: group || undefined,
			sources: source ? [source] : [],
			units: [
				...new Set(
					units
						.split(',')
						.map((u) => u.trim())
						.filter(Boolean)
				)
			].slice(0, MAX_UNITS),
			severity: severity || undefined,
			text: cutBytes(text, MAX_TEXT_BYTES),
			ignoreCase
		};
	}

	function submit(event: SubmitEvent) {
		event.preventDefault();
		onApply(current());
	}

	function preset(p: LogPreset) {
		onApply({ ...current(), preset: p, from: undefined, to: undefined });
	}
</script>

<form class="flex flex-col gap-3" onsubmit={submit} aria-label={m.logs_filters()}>
	<div class="flex flex-wrap items-center gap-2">
		<div role="group" aria-label={m.range_label()} class="inline-flex flex-wrap gap-1">
			{#each logPresets as p (p)}
				{@const active = !absolute && filter.preset === p}
				<Button
					type="button"
					variant={active ? 'secondary' : 'ghost'}
					size="sm"
					aria-pressed={active}
					onclick={() => preset(p)}
				>
					{presetLabels[p]()}
				</Button>
			{/each}
		</div>
		{#if absolute}
			<span class="text-sm text-muted-foreground">
				{m.logs_range_absolute({ from: dateTime(filter.from!), to: dateTime(filter.to!) })}
			</span>
			<Button type="button" variant="outline" size="sm" onclick={() => preset(filter.preset)}>
				<ZoomOut aria-hidden="true" />
				{m.logs_range_back({ preset: presetLabels[filter.preset]() })}
			</Button>
		{/if}
	</div>

	<div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
		{#if !fixedNode}
			<div class="flex flex-col gap-1.5">
				<Label for="logs-node">{m.logs_filter_node()}</Label>
				<select id="logs-node" class={select} bind:value={node}>
					<option value="">{m.logs_filter_all_nodes()}</option>
					{#if filter.nodes.length > 1}
						<option value="many">{m.logs_filter_many_nodes({ count: filter.nodes.length })}</option>
					{/if}
					{#each nodes as n (n.nodeId)}
						<option value={n.nodeId}>{n.hostname}</option>
					{/each}
				</select>
			</div>
			<div class="flex flex-col gap-1.5">
				<Label for="logs-group">{m.logs_filter_group()}</Label>
				<select id="logs-group" class={select} bind:value={group}>
					<option value="">{m.logs_filter_all_groups()}</option>
					{#each groups as [id, name] (id)}
						<option value={id}>{name}</option>
					{/each}
				</select>
			</div>
		{/if}
		<div class="flex flex-col gap-1.5">
			<Label for="logs-source">{m.logs_filter_source()}</Label>
			<select id="logs-source" class={select} bind:value={source}>
				<option value="">{m.logs_filter_all_sources()}</option>
				<option value="journal">{sourceLabel('journal')}</option>
				<option value="file">{sourceLabel('file')}</option>
				<option value="container">{sourceLabel('container')}</option>
			</select>
		</div>
		<div class="flex flex-col gap-1.5">
			<Label for="logs-severity">{m.logs_filter_severity()}</Label>
			<select id="logs-severity" class={select} bind:value={severity}>
				<option value="">{m.logs_filter_all_severities()}</option>
				{#each severities as s (s)}
					<option value={s}>{m.logs_filter_at_least({ severity: severityNameLabel(s) })}</option>
				{/each}
			</select>
		</div>
		<div class="flex flex-col gap-1.5">
			<Label for="logs-units">{m.logs_filter_units()}</Label>
			<Input
				id="logs-units"
				bind:value={units}
				placeholder="nginx.service, cron"
				autocomplete="off"
				spellcheck={false}
				aria-describedby="logs-units-hint"
			/>
			<p id="logs-units-hint" class="text-xs text-muted-foreground">{m.logs_filter_units_hint()}</p>
		</div>
		<div class="flex flex-col gap-1.5 sm:col-span-2 lg:col-span-2">
			<Label for="logs-text">{m.logs_filter_text()}</Label>
			<Input
				id="logs-text"
				type="search"
				bind:value={text}
				maxlength={MAX_TEXT_BYTES}
				autocomplete="off"
				spellcheck={false}
				aria-describedby="logs-text-hint"
			/>
			<p id="logs-text-hint" class="text-xs text-muted-foreground">{m.logs_filter_text_hint()}</p>
		</div>
		<label class="flex items-center gap-2 self-center text-sm">
			<input type="checkbox" bind:checked={ignoreCase} class="size-4 accent-primary" />
			{m.logs_filter_ignore_case()}
		</label>
	</div>

	<div class="flex flex-wrap gap-2">
		<Button type="submit" size="sm">
			<Search aria-hidden="true" />
			{m.logs_search()}
		</Button>
		<Button
			type="button"
			variant="ghost"
			size="sm"
			onclick={() => onApply({ ...emptyFilter(), preset: filter.preset })}
		>
			<RotateCcw aria-hidden="true" />
			{m.logs_filter_reset()}
		</Button>
	</div>
</form>
