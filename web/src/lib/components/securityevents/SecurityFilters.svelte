<script lang="ts">
	import RotateCcw from '@lucide/svelte/icons/rotate-ccw';
	import Search from '@lucide/svelte/icons/search';
	import ZoomOut from '@lucide/svelte/icons/zoom-out';
	import type { NodeSummary } from '#lib/api/gen/sermon/web/v1/fleet_pb.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { cutBytes } from '#lib/logs/filter.js';
	import { m } from '#lib/paraglide/messages.js';
	import {
		emptySecurityFilter,
		eventKinds,
		failedKinds,
		MAX_NETWORK,
		MAX_USER_BYTES,
		securityPresets,
		sudoFailureKinds,
		validNetwork,
		type EventKind,
		type SecurityFilter,
		type SecurityPreset
	} from '#lib/security/filter.js';
	import { kindLabel, securityPresetLabels } from '#lib/security/labels.js';

	/**
	 * The filters of the security dashboard. Ranges apply at once; the other fields apply with
	 * "Search" (or Enter).
	 */
	let {
		filter,
		nodes,
		fixedNode = false,
		onApply
	}: {
		filter: SecurityFilter;
		nodes: NodeSummary[];
		/** On a node page: no node choice. */
		fixedNode?: boolean;
		onApply: (filter: SecurityFilter) => void;
	} = $props();

	const select =
		'h-9 rounded-md border border-input bg-background px-2 text-sm focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none';

	/** Kind choices: everything, the groups of the overview, then single kinds. */
	const groups: Record<string, EventKind[]> = { failed: failedKinds, sudo: sudoFailureKinds };

	let node = $state('');
	let kind = $state('');
	let user = $state('');
	let network = $state('');
	let newOnly = $state(false);
	let networkError = $state(false);

	const same = (a: EventKind[], b: EventKind[]) =>
		a.length === b.length && a.every((k) => b.includes(k));

	// The form follows the applied filter (URL, links, clicks in the overview).
	$effect.pre(() => {
		node = filter.nodes.length === 1 ? filter.nodes[0] : filter.nodes.length > 1 ? 'many' : '';
		kind =
			filter.kinds.length === 0
				? ''
				: (Object.keys(groups).find((g) => same(groups[g], filter.kinds)) ??
					(filter.kinds.length === 1 ? filter.kinds[0] : 'many'));
		user = filter.user ?? '';
		network = filter.network;
		newOnly = filter.newOnly;
		networkError = false;
	});

	const absolute = $derived(filter.from !== undefined && filter.to !== undefined);
	const dateTime = (ms: number) =>
		new Intl.DateTimeFormat(localeState.current, { dateStyle: 'short', timeStyle: 'short' }).format(
			ms
		);

	function current(): SecurityFilter {
		return {
			...filter,
			nodes: node === 'many' ? filter.nodes : node ? [node] : [],
			kinds: kind === 'many' ? filter.kinds : kind ? (groups[kind] ?? [kind as EventKind]) : [],
			// An empty field is no filter; the empty name only comes from a click on it.
			user: user
				? cutBytes(user, MAX_USER_BYTES)
				: filter.user === '' && user === ''
					? ''
					: undefined,
			network: network.trim(),
			newOnly
		};
	}

	function submit(event: SubmitEvent) {
		event.preventDefault();
		if (!validNetwork(network.trim())) {
			networkError = true;
			return;
		}
		onApply(current());
	}

	function preset(p: SecurityPreset) {
		onApply({ ...filter, preset: p, from: undefined, to: undefined });
	}
</script>

<form class="flex flex-col gap-3" onsubmit={submit} aria-label={m.secdash_filters()}>
	<div class="flex flex-wrap items-center gap-2">
		<div role="group" aria-label={m.range_label()} class="inline-flex flex-wrap gap-1">
			{#each securityPresets as p (p)}
				{@const active = !absolute && filter.preset === p}
				<Button
					type="button"
					variant={active ? 'secondary' : 'ghost'}
					size="sm"
					aria-pressed={active}
					onclick={() => preset(p)}
				>
					{securityPresetLabels[p]()}
				</Button>
			{/each}
		</div>
		{#if absolute}
			<span class="text-sm text-muted-foreground">
				{m.logs_range_absolute({ from: dateTime(filter.from!), to: dateTime(filter.to!) })}
			</span>
			<Button type="button" variant="outline" size="sm" onclick={() => preset(filter.preset)}>
				<ZoomOut aria-hidden="true" />
				{m.logs_range_back({ preset: securityPresetLabels[filter.preset]() })}
			</Button>
		{/if}
	</div>

	<div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
		{#if !fixedNode}
			<div class="flex flex-col gap-1.5">
				<Label for="sec-node">{m.logs_filter_node()}</Label>
				<select id="sec-node" class={select} bind:value={node}>
					<option value="">{m.logs_filter_all_nodes()}</option>
					{#if filter.nodes.length > 1}
						<option value="many">{m.logs_filter_many_nodes({ count: filter.nodes.length })}</option>
					{/if}
					{#each nodes as n (n.nodeId)}
						<option value={n.nodeId}>{n.hostname}</option>
					{/each}
				</select>
			</div>
		{/if}
		<div class="flex flex-col gap-1.5">
			<Label for="sec-kind">{m.secdash_filter_kind()}</Label>
			<select id="sec-kind" class={select} bind:value={kind}>
				<option value="">{m.secdash_filter_all_kinds()}</option>
				<option value="failed">{m.secdash_filter_failed()}</option>
				<option value="sudo">{m.secdash_filter_sudo()}</option>
				{#if kind === 'many'}
					<option value="many">{m.secdash_filter_many_kinds({ count: filter.kinds.length })}</option
					>
				{/if}
				{#each eventKinds as k (k)}
					<option value={k}>{kindLabel(k)}</option>
				{/each}
			</select>
		</div>
		<div class="flex flex-col gap-1.5">
			<Label for="sec-user">{m.secdash_filter_user()}</Label>
			<Input id="sec-user" bind:value={user} autocomplete="off" spellcheck={false} />
		</div>
		<div class="flex flex-col gap-1.5">
			<Label for="sec-network">{m.secdash_filter_network()}</Label>
			<Input
				id="sec-network"
				bind:value={network}
				maxlength={MAX_NETWORK}
				placeholder="192.0.2.0/24"
				autocomplete="off"
				spellcheck={false}
				aria-invalid={networkError}
				aria-describedby="sec-network-hint"
			/>
			<p
				id="sec-network-hint"
				class="text-xs {networkError ? 'text-destructive' : 'text-muted-foreground'}"
				role={networkError ? 'alert' : undefined}
			>
				{networkError ? m.secdash_filter_network_invalid() : m.secdash_filter_network_hint()}
			</p>
		</div>
		<label class="flex items-center gap-2 self-center text-sm">
			<input type="checkbox" bind:checked={newOnly} class="size-4 accent-primary" />
			{m.secdash_filter_new_only()}
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
			onclick={() => onApply({ ...emptySecurityFilter(), preset: filter.preset })}
		>
			<RotateCcw aria-hidden="true" />
			{m.logs_filter_reset()}
		</Button>
	</div>
</form>
