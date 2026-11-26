<script lang="ts">
	import { clone } from '@bufbuild/protobuf';
	import { Code, ConnectError } from '@connectrpc/connect';
	import Download from '@lucide/svelte/icons/download';
	import Pencil from '@lucide/svelte/icons/pencil';
	import Plus from '@lucide/svelte/icons/plus';
	import Save from '@lucide/svelte/icons/save';
	import Share2 from '@lucide/svelte/icons/share-2';
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import { api } from '#lib/api/client.js';
	import { dashboardsApi } from '#lib/api/dashboards.js';
	import {
		DashboardSpecSchema,
		VariableKind,
		WidgetKind,
		type Dashboard,
		type DashboardSpec,
		type Widget
	} from '#lib/api/gen/sermon/web/v1/dashboard_pb.js';
	import DashboardGrid from '#lib/components/dashboards/DashboardGrid.svelte';
	import WidgetView from '#lib/components/dashboards/WidgetView.svelte';
	import { kindLabel, type CatalogMetric } from '#lib/components/dashboards/labels.js';
	import FleetNav from '#lib/components/fleet/FleetNav.svelte';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import TimeRangePicker from '#lib/components/time/TimeRangePicker.svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { saveFile } from '#lib/download/download.js';
	import type { Box, Item } from '#lib/dashboards/layout.js';
	import {
		kinds,
		layoutOf,
		MAX_WIDGETS,
		newWidget,
		presetOf,
		timePresetOf,
		withLayout
	} from '#lib/dashboards/model.js';
	import { sessionSource } from '#lib/dashboards/session.js';
	import { resolveVariables, withVariable, type FleetNode } from '#lib/dashboards/variables.js';
	import { displayText } from '#lib/logs/text.js';
	import { m } from '#lib/paraglide/messages.js';
	import { presets, timeRange, type Preset } from '#lib/stores/timerange.svelte.js';

	/** A custom dashboard (KONZEPT §16, M5-24): view, and for whoever may edit it the grid editor. */
	const id = $derived(page.params.id ?? '');
	let dashboard = $state.raw<Dashboard | undefined>();
	let status = $state<'loading' | 'ready' | 'missing' | 'error'>('loading');
	let draft = $state.raw<DashboardSpec | undefined>();
	let busy = $state(false);
	let failure = $state('');
	let conflict = $state(false);
	let confirmDelete = $state(false);
	let addKind = $state(WidgetKind.TIMESERIES);
	/** The widget in the form: a new one or a copy of one of the draft. */
	let editing = $state.raw<{ widget: Widget; box: Box; isNew: boolean } | undefined>();
	let catalog = $state.raw<CatalogMetric[]>([]);
	/** The nodes the viewer sees: variables, groups and host names. */
	let fleet = $state.raw<FleetNode[]>([]);
	/** Live ranges move on this often (KONZEPT §17). */
	const REFRESH = 30_000;

	async function load() {
		status = 'loading';
		conflict = false;
		try {
			const [res] = await Promise.all([
				dashboardsApi.getDashboard({ dashboardId: id }),
				loadFleet()
			]);
			dashboard = res.dashboard;
			if (dashboard?.spec) timeRange.setPreset(presetOf(dashboard.spec.timeRange));
			status = 'ready';
			if (page.url.searchParams.get('edit') === '1' && dashboard?.canEdit) startEdit();
		} catch (error) {
			status = ConnectError.from(error).code === Code.NotFound ? 'missing' : 'error';
		}
	}

	$effect(() => {
		if (id) void load();
	});

	/** Without the fleet the widgets of groups and variables stay empty; the page still works. */
	async function loadFleet() {
		try {
			fleet = (await api.fleet.getFleetSummary({})).nodes.map((n) => ({
				id: n.nodeId,
				hostname: n.hostname,
				groupId: n.groupId,
				groupName: n.groupName
			}));
		} catch {
			fleet = [];
		}
	}

	$effect(() => {
		const timer = setInterval(() => timeRange.refresh(), REFRESH);
		return () => clearInterval(timer);
	});

	const spec = $derived(draft ?? dashboard?.spec);
	const layout = $derived(spec ? layoutOf(spec) : []);
	const titles = $derived(Object.fromEntries((spec?.widgets ?? []).map((w) => [w.id, w.title])));
	const values = $derived(spec ? resolveVariables(spec, page.url.searchParams, fleet) : {});
	const source = sessionSource({ fleet: () => fleet, values: () => values });
	const groupsOf = $derived(
		[...new Map(fleet.map((n) => [n.groupId, n.groupName])).entries()].sort((a, b) =>
			a[1].localeCompare(b[1])
		)
	);
	const nodesByName = $derived([...fleet].sort((a, b) => a.hostname.localeCompare(b.hostname)));

	function choose(name: string, value: string) {
		// eslint-disable-next-line svelte/no-navigation-without-resolve -- the same page with the choice in its query
		void goto(withVariable(page.url, name, value), {
			replace: true,
			reset: false
		});
	}

	/** The pixels a widget's content may use: its rows minus header and padding. */
	function contentHeight(rows: number): number {
		return rows * 64 + (rows - 1) * 12 - 46;
	}

	function startEdit() {
		if (!dashboard?.spec) return;
		draft = clone(DashboardSpecSchema, dashboard.spec);
		failure = '';
		void loadChoices();
	}

	function cancelEdit() {
		draft = undefined;
		editing = undefined;
		failure = '';
		conflict = false;
		void goto(resolve('/(app)/dashboards/[id]', { id }), { replace: true, reset: false });
	}

	/** The metric catalog and the nodes for the widget form, once. */
	async function loadChoices() {
		if (catalog.length > 0) return;
		try {
			const metrics = await api.metrics.listMetrics({});
			catalog = metrics.metrics.map((d) => ({ name: d.name, labels: d.labels, area: d.area }));
		} catch {
			failure = m.dashboard_choices_failed();
		}
	}

	function setDraft(next: Partial<DashboardSpec>) {
		if (draft) draft = { ...draft, ...next };
	}

	function addWidget() {
		if (!draft) return;
		const w = newWidget(draft, addKind, kindLabel(addKind));
		if (!w) {
			failure = m.dashboard_full({ max: MAX_WIDGETS });
			return;
		}
		editing = { widget: w, box: { ...w.position! }, isNew: true };
	}

	function editWidget(widgetId: string) {
		const w = draft?.widgets.find((x) => x.id === widgetId);
		if (w?.position) editing = { widget: w, box: { ...w.position }, isNew: false };
	}

	function applyWidget(w: Widget) {
		if (!draft || !editing) return;
		const widgets = editing.isNew
			? [...draft.widgets, w]
			: draft.widgets.map((x) => (x.id === w.id ? w : x));
		setDraft({ widgets });
		editing = undefined;
	}

	function removeWidget(widgetId: string) {
		if (draft) setDraft({ widgets: draft.widgets.filter((w) => w.id !== widgetId) });
	}

	function setLayout(items: Item[]) {
		if (draft) draft = withLayout(draft, items);
	}

	function explain(error: unknown): string {
		const err = ConnectError.from(error);
		return err.code === Code.PermissionDenied ? m.dashboard_forbidden() : err.rawMessage;
	}

	async function save() {
		if (!draft || !dashboard) return;
		busy = true;
		failure = '';
		try {
			const res = await dashboardsApi.updateDashboard({
				dashboardId: dashboard.id,
				revision: dashboard.revision,
				spec: draft
			});
			dashboard = res.dashboard;
			draft = undefined;
			void goto(resolve('/(app)/dashboards/[id]', { id }), { replace: true, reset: false });
		} catch (error) {
			if (ConnectError.from(error).code === Code.Aborted) conflict = true;
			else failure = m.dashboard_save_failed({ reason: explain(error) });
		} finally {
			busy = false;
		}
	}

	async function share(shared: boolean) {
		if (!dashboard) return;
		busy = true;
		failure = '';
		try {
			dashboard = (await dashboardsApi.setDashboardShared({ dashboardId: dashboard.id, shared }))
				.dashboard;
		} catch (error) {
			failure = m.dashboard_share_failed({ reason: explain(error) });
		} finally {
			busy = false;
		}
	}

	async function exportJson() {
		if (!dashboard) return;
		failure = '';
		try {
			const res = await dashboardsApi.exportDashboard({ dashboardId: dashboard.id });
			const name = (dashboard.spec?.title ?? 'dashboard')
				.toLowerCase()
				.replace(/[^a-z0-9]+/g, '-')
				.replace(/^-|-$/g, '');
			saveFile(res.json, `${name || 'dashboard'}.json`, 'application/json');
		} catch (error) {
			failure = m.dashboard_export_failed({ reason: explain(error) });
		}
	}

	async function remove() {
		if (!dashboard) return;
		if (!confirmDelete) {
			confirmDelete = true;
			return;
		}
		busy = true;
		try {
			await dashboardsApi.deleteDashboard({ dashboardId: dashboard.id });
			await goto(resolve('/(app)/dashboards'));
		} catch (error) {
			failure = m.dashboard_delete_failed({ reason: explain(error) });
			confirmDelete = false;
		} finally {
			busy = false;
		}
	}

	const selectClass =
		'h-9 rounded-md border border-input bg-background px-2 text-sm focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none';
	const rangeLabels: Record<Preset, () => string> = {
		'15m': () => m.range_15m(),
		'1h': () => m.range_1h(),
		'6h': () => m.range_6h(),
		'24h': () => m.range_24h(),
		'7d': () => m.range_7d(),
		'30d': () => m.range_30d()
	};
</script>

<svelte:head>
	<title>{displayText(spec?.title || m.dashboards_title())} · SerMon</title>
</svelte:head>

<FleetNav />

{#if status === 'loading'}
	<p role="status" class="pt-4 text-muted-foreground">{m.loading()}</p>
{:else if status === 'missing'}
	<div class="flex flex-col items-start gap-2 pt-4">
		<p role="alert">{m.dashboard_missing()}</p>
		<Button variant="outline" href={resolve('/(app)/dashboards')}>{m.dashboard_back()}</Button>
	</div>
{:else if status === 'error' || !dashboard || !spec}
	<div class="flex flex-col items-start gap-2 pt-4">
		<p role="alert" class="text-destructive">{m.error_unavailable()}</p>
		<Button variant="outline" onclick={load}>{m.retry()}</Button>
	</div>
{:else}
	<div class="flex flex-col gap-4 pt-4">
		<nav aria-label={m.dashboard_breadcrumb()} class="text-sm">
			<a href={resolve('/(app)/dashboards')} class="text-muted-foreground hover:underline"
				>{m.dashboards_title()}</a
			>
		</nav>
		{#if draft}
			<h1 class="sr-only">
				{m.dashboard_editing({ title: displayText(dashboard.spec?.title ?? '') })}
			</h1>
			<div class="flex flex-wrap items-end gap-3">
				<div class="flex min-w-64 flex-1 flex-col gap-1">
					<Label for="dashboard-name">{m.dashboard_name()}</Label>
					<Input
						id="dashboard-name"
						maxlength={200}
						value={draft.title}
						oninput={(e) => setDraft({ title: e.currentTarget.value })}
					/>
				</div>
				<div class="flex flex-col gap-1">
					<Label for="dashboard-range">{m.dashboard_default_range()}</Label>
					<select
						id="dashboard-range"
						class={selectClass}
						value={presetOf(draft.timeRange)}
						onchange={(e) => setDraft({ timeRange: timePresetOf(e.currentTarget.value as Preset) })}
					>
						{#each presets as p (p)}<option value={p}>{rangeLabels[p]()}</option>{/each}
					</select>
				</div>
				<div class="flex w-full flex-col gap-1">
					<Label for="dashboard-description">{m.dashboard_description()}</Label>
					<textarea
						id="dashboard-description"
						class="min-h-16 rounded-md border border-input bg-background px-3 py-2 text-sm focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
						maxlength={2000}
						value={draft.description}
						oninput={(e) => setDraft({ description: e.currentTarget.value })}></textarea>
				</div>
			</div>
			<div
				class="flex flex-wrap items-end gap-2"
				role="toolbar"
				aria-label={m.dashboard_editor_toolbar()}
			>
				<div class="flex flex-col gap-1">
					<Label for="dashboard-add-kind">{m.dashboard_add_kind()}</Label>
					<select id="dashboard-add-kind" class={selectClass} bind:value={addKind}>
						{#each kinds as k (k.kind)}<option value={k.kind}>{kindLabel(k.kind)}</option>{/each}
					</select>
				</div>
				<Button
					variant="outline"
					onclick={addWidget}
					disabled={draft.widgets.length >= MAX_WIDGETS}
				>
					<Plus aria-hidden="true" />
					{m.dashboard_add_widget()}
				</Button>
				<span class="ml-auto flex gap-2">
					<Button variant="outline" onclick={cancelEdit} disabled={busy}
						>{m.dashboard_cancel()}</Button
					>
					<Button onclick={save} disabled={busy}>
						<Save aria-hidden="true" />
						{m.dashboard_save()}
					</Button>
				</span>
			</div>
		{:else}
			<div class="flex flex-wrap items-baseline gap-x-3 gap-y-2">
				<h1 class="text-2xl font-semibold tracking-tight">{displayText(spec.title)}</h1>
				<HelpTip topic="pages/dashboard" />
				{#if dashboard.shared}
					<span class="inline-flex items-center gap-1 rounded-full border px-2 text-xs">
						<Share2 class="size-3" aria-hidden="true" />
						{m.dashboards_shared()}
					</span>
				{/if}
				<span class="ml-auto flex flex-wrap gap-2">
					{#if dashboard.canEdit}
						<Button variant="outline" size="sm" onclick={startEdit}>
							<Pencil aria-hidden="true" />
							{m.dashboard_edit()}
						</Button>
						<Button
							variant="outline"
							size="sm"
							disabled={busy}
							aria-pressed={dashboard.shared}
							onclick={() => share(!dashboard?.shared)}
						>
							<Share2 aria-hidden="true" />
							{dashboard.shared ? m.dashboard_unshare() : m.dashboard_share()}
						</Button>
					{/if}
					<Button variant="outline" size="sm" onclick={exportJson}>
						<Download aria-hidden="true" />
						{m.dashboard_export()}
					</Button>
					{#if dashboard.canEdit}
						<Button
							variant={confirmDelete ? 'destructive' : 'ghost'}
							size="sm"
							disabled={busy}
							onclick={remove}
						>
							{confirmDelete ? m.dashboard_delete_confirm() : m.dashboard_delete()}
						</Button>
					{/if}
				</span>
			</div>
			{#if spec.description}
				<p class="max-w-3xl text-sm whitespace-pre-line text-muted-foreground">
					{displayText(spec.description)}
				</p>
			{/if}
		{/if}

		{#if conflict}
			<div
				role="alert"
				class="flex flex-wrap items-center gap-2 rounded-md border border-destructive p-3 text-sm"
			>
				{m.dashboard_conflict()}
				<Button variant="outline" size="sm" onclick={load}>{m.dashboard_reload()}</Button>
			</div>
		{/if}
		{#if failure}
			<p role="alert" class="text-sm text-destructive">{failure}</p>
		{/if}

		<div class="flex flex-wrap items-end gap-3">
			<TimeRangePicker />
			{#if !draft}
				{#each spec.variables as v (v.name)}
					<div class="flex flex-col gap-1">
						<Label for="var-{v.name}">{v.name}</Label>
						<select
							id="var-{v.name}"
							class={selectClass}
							value={values[v.name] ?? ''}
							onchange={(e) => choose(v.name, e.currentTarget.value)}
						>
							{#if v.kind === VariableKind.GROUP}
								{#each groupsOf as [gid, name] (gid)}<option value={gid}>{displayText(name)}</option
									>{/each}
							{:else}
								{#each nodesByName as n (n.id)}<option value={n.id}
										>{displayText(n.hostname)}</option
									>{/each}
							{/if}
						</select>
					</div>
				{/each}
			{/if}
		</div>
		{#if draft}
			{#await import('#lib/components/dashboards/VariablesEditor.svelte') then { default: VariablesEditor }}
				<VariablesEditor
					variables={draft.variables}
					{fleet}
					onchange={(variables) => setDraft({ variables })}
				/>
			{/await}
		{/if}

		{#if spec.widgets.length === 0}
			<div class="rounded-lg border border-dashed p-6 text-sm text-muted-foreground">
				{draft ? m.dashboard_empty_editing() : m.dashboard_empty()}
			</div>
		{:else}
			<DashboardGrid
				items={layout}
				{titles}
				label={m.dashboard_grid_label({ title: displayText(spec.title) })}
				editable={draft !== undefined}
				onchange={setLayout}
				onedit={editWidget}
				onremove={removeWidget}
			>
				{#snippet widget(widgetId)}
					{@const w = spec.widgets.find((x) => x.id === widgetId)}
					{#if w && draft}
						<p class="text-xs text-muted-foreground">
							{kindLabel(w.kind)}{w.query?.metric ? ` · ${w.query.metric}` : ''}
						</p>
					{:else if w}
						<WidgetView
							widget={w}
							{source}
							range={timeRange.range}
							height={contentHeight(w.position?.h ?? 1)}
						/>
					{/if}
				{/snippet}
			</DashboardGrid>
		{/if}
	</div>

	{#if editing && draft}
		{#await import('#lib/components/dashboards/WidgetForm.svelte') then { default: WidgetForm }}
			<WidgetForm
				widget={editing.widget}
				box={editing.box}
				variables={draft.variables}
				metrics={catalog}
				nodes={fleet}
				onsave={applyWidget}
				oncancel={() => (editing = undefined)}
			/>
		{/await}
	{/if}
{/if}
