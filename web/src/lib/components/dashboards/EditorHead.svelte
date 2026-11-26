<script lang="ts">
	import Plus from '@lucide/svelte/icons/plus';
	import Save from '@lucide/svelte/icons/save';
	import { WidgetKind, type DashboardSpec } from '#lib/api/gen/sermon/web/v1/dashboard_pb.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { kinds, MAX_WIDGETS, presetOf, timePresetOf } from '#lib/dashboards/model.js';
	import { displayText } from '#lib/logs/text.js';
	import { m } from '#lib/paraglide/messages.js';
	import { presets, type Preset } from '#lib/stores/timerange.svelte.js';
	import { kindLabel } from './labels.js';

	/** The head of the editor (M5-24): title, range, description and the toolbar; loaded on demand. */
	let {
		title,
		draft,
		busy,
		setDraft,
		addWidget,
		cancelEdit,
		save
	}: {
		title: string;
		draft: DashboardSpec;
		busy: boolean;
		setDraft: (next: Partial<DashboardSpec>) => void;
		addWidget: (kind: WidgetKind) => void;
		cancelEdit: () => void;
		save: () => void;
	} = $props();

	let addKind = $state(WidgetKind.TIMESERIES);
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

<h1 class="sr-only">
	{m.dashboard_editing({ title: displayText(title) })}
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
		onclick={() => addWidget(addKind)}
		disabled={draft.widgets.length >= MAX_WIDGETS}
	>
		<Plus aria-hidden="true" />
		{m.dashboard_add_widget()}
	</Button>
	<span class="ml-auto flex gap-2">
		<Button variant="outline" onclick={cancelEdit} disabled={busy}>{m.dashboard_cancel()}</Button>
		<Button onclick={save} disabled={busy}>
			<Save aria-hidden="true" />
			{m.dashboard_save()}
		</Button>
	</span>
</div>
