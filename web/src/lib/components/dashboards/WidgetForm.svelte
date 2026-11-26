<script lang="ts">
	import type { Widget, DashboardVariable } from '#lib/api/gen/sermon/web/v1/dashboard_pb.js';
	import { VariableKind, WidgetKind } from '#lib/api/gen/sermon/web/v1/dashboard_pb.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Dialog from '#lib/components/ui/dialog/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import {
		formOf,
		MAX_TEXT,
		widgetOf,
		type FormError,
		type WidgetForm
	} from '#lib/dashboards/form.js';
	import type { Box } from '#lib/dashboards/layout.js';
	import { isMetricKind, kinds, MAX_NODES, singleNode } from '#lib/dashboards/model.js';
	import { displayText } from '#lib/logs/text.js';
	import { m } from '#lib/paraglide/messages.js';
	import { kindLabel, type CatalogMetric, type NodeChoice } from './labels.js';

	/** The form of one widget (ADR 0040), loaded on demand by the editor. */
	let {
		widget,
		box,
		variables,
		metrics,
		nodes,
		onsave,
		oncancel
	}: {
		widget: Widget;
		box: Box;
		variables: DashboardVariable[];
		metrics: CatalogMetric[];
		nodes: NodeChoice[];
		onsave: (w: Widget) => void;
		oncancel: () => void;
	} = $props();

	// The form starts from the widget once; the editor gives every opening its own instance.
	// svelte-ignore state_referenced_locally
	let form = $state<WidgetForm>(formOf(widget));
	let error = $state<FormError | undefined>();

	const metric = $derived(metrics.find((d) => d.name === form.metric));
	const areas = $derived([...new Set(metrics.map((d) => d.area))].sort());
	const groups = $derived(
		[...new Map(nodes.map((n) => [n.groupId, n.groupName])).entries()].sort((a, b) =>
			a[1].localeCompare(b[1])
		)
	);
	const single = $derived(singleNode(form.kind));
	const errors: Record<FormError, () => string> = {
		title: () => m.dashboard_form_error_title(),
		metric: () => m.dashboard_form_error_metric(),
		target: () => m.dashboard_form_error_target(),
		nodes: () => m.dashboard_form_error_nodes({ max: MAX_NODES }),
		single: () => m.dashboard_form_error_single(),
		text: () => m.dashboard_form_error_text({ max: MAX_TEXT })
	};
	const severities = [
		{ value: 0, label: () => m.dashboard_form_severity_all() },
		{ value: 4, label: () => m.dashboard_form_severity_error() },
		{ value: 5, label: () => m.dashboard_form_severity_warning() },
		{ value: 7, label: () => m.dashboard_form_severity_info() }
	];
	const selectClass =
		'h-9 rounded-md border border-input bg-background px-2 text-sm focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none';

	function toggleNode(id: string, on: boolean) {
		if (single) form.nodeIds = on ? [id] : [];
		else form.nodeIds = on ? [...form.nodeIds, id] : form.nodeIds.filter((n) => n !== id);
	}

	function submit(event: SubmitEvent) {
		event.preventDefault();
		const result = widgetOf(form, box, variables);
		error = result.error;
		if (result.widget) onsave(result.widget);
	}
</script>

<Dialog.Root
	open
	onOpenChange={(open) => {
		if (!open) oncancel();
	}}
>
	<Dialog.Content
		closeLabel={m.dashboard_form_cancel()}
		class="max-h-[90vh] overflow-auto sm:max-w-2xl"
	>
		<Dialog.Header>
			<Dialog.Title>{m.dashboard_form_title()}</Dialog.Title>
			<Dialog.Description>{m.dashboard_form_text()}</Dialog.Description>
		</Dialog.Header>
		<form class="flex flex-col gap-4" onsubmit={submit} novalidate>
			<div class="flex flex-wrap gap-3">
				<div class="flex flex-col gap-1">
					<Label for="widget-kind">{m.dashboard_form_kind()}</Label>
					<select id="widget-kind" class={selectClass} bind:value={form.kind}>
						{#each kinds as k (k.kind)}<option value={k.kind}>{kindLabel(k.kind)}</option>{/each}
					</select>
				</div>
				<div class="flex min-w-64 flex-1 flex-col gap-1">
					<Label for="widget-title">{m.dashboard_form_name()}</Label>
					<Input
						id="widget-title"
						maxlength={200}
						bind:value={form.title}
						aria-invalid={error === 'title' || undefined}
					/>
				</div>
			</div>

			{#if isMetricKind(form.kind)}
				<div class="flex flex-col gap-1">
					<Label for="widget-metric">{m.dashboard_form_metric()}</Label>
					<select
						id="widget-metric"
						class={selectClass}
						bind:value={form.metric}
						onchange={() => (form.labels = {})}
						aria-invalid={error === 'metric' || undefined}
					>
						<option value="">{m.dashboard_form_metric_choose()}</option>
						{#each areas as area (area)}
							<optgroup label={area}>
								{#each metrics.filter((d) => d.area === area) as d (d.name)}
									<option value={d.name}>{d.name}</option>
								{/each}
							</optgroup>
						{/each}
					</select>
				</div>
				{#if metric && metric.labels.length > 0}
					<fieldset class="flex flex-wrap gap-3 rounded-md border p-3">
						<legend class="px-1 text-sm font-medium">{m.dashboard_form_labels()}</legend>
						{#each metric.labels as name (name)}
							<div class="flex flex-col gap-1">
								<Label for="widget-label-{name}">{name}</Label>
								<Input
									id="widget-label-{name}"
									class="w-40"
									maxlength={256}
									placeholder={m.dashboard_form_label_any()}
									value={form.labels[name] ?? ''}
									oninput={(e) => (form.labels = { ...form.labels, [name]: e.currentTarget.value })}
								/>
							</div>
						{/each}
					</fieldset>
				{/if}
			{/if}

			<fieldset class="flex flex-col gap-2 rounded-md border p-3">
				<legend class="px-1 text-sm font-medium">{m.dashboard_form_target()}</legend>
				<div class="flex flex-wrap gap-3 text-sm">
					<label class="flex items-center gap-1">
						<input type="radio" name="target" value="nodes" bind:group={form.target} />
						{single ? m.dashboard_form_target_node() : m.dashboard_form_target_nodes()}
					</label>
					{#if !single}
						<label class="flex items-center gap-1">
							<input type="radio" name="target" value="group" bind:group={form.target} />
							{m.dashboard_form_target_group()}
						</label>
					{/if}
					{#if variables.length > 0}
						<label class="flex items-center gap-1">
							<input type="radio" name="target" value="variable" bind:group={form.target} />
							{m.dashboard_form_target_variable()}
						</label>
					{/if}
					{#if form.kind === WidgetKind.ALERT_LIST}
						<label class="flex items-center gap-1">
							<input type="radio" name="target" value="all" bind:group={form.target} />
							{m.dashboard_form_target_all()}
						</label>
					{/if}
				</div>
				{#if form.target === 'nodes'}
					{#if nodes.length === 0}
						<p class="text-sm text-muted-foreground">{m.dashboard_form_no_nodes()}</p>
					{:else}
						<ul
							class="flex max-h-48 flex-col gap-1 overflow-auto text-sm"
							aria-label={m.dashboard_form_target_nodes()}
						>
							{#each nodes as n (n.id)}
								<li>
									<label class="flex items-center gap-2">
										<input
											type={single ? 'radio' : 'checkbox'}
											name="widget-nodes"
											checked={form.nodeIds.includes(n.id)}
											onchange={(e) => toggleNode(n.id, e.currentTarget.checked)}
										/>
										<span class="truncate">{displayText(n.hostname)}</span>
										<span class="text-muted-foreground">{displayText(n.groupName)}</span>
									</label>
								</li>
							{/each}
						</ul>
					{/if}
				{:else if form.target === 'group'}
					<div class="flex flex-col gap-1">
						<Label for="widget-group">{m.dashboard_form_target_group()}</Label>
						<select id="widget-group" class={selectClass} bind:value={form.groupId}>
							<option value="">{m.dashboard_form_choose()}</option>
							{#each groups as [id, name] (id)}<option value={id}>{displayText(name)}</option
								>{/each}
						</select>
					</div>
				{:else if form.target === 'variable'}
					<div class="flex flex-col gap-1">
						<Label for="widget-variable">{m.dashboard_form_target_variable()}</Label>
						<select id="widget-variable" class={selectClass} bind:value={form.variable}>
							<option value="">{m.dashboard_form_choose()}</option>
							{#each variables.filter((v) => !single || v.kind === VariableKind.NODE) as v (v.name)}
								<option value={v.name}>{v.name}</option>
							{/each}
						</select>
					</div>
				{/if}
			</fieldset>

			{#if form.kind === WidgetKind.LOG_STREAM}
				<div class="flex flex-wrap gap-3">
					<div class="flex min-w-64 flex-1 flex-col gap-1">
						<Label for="widget-text">{m.dashboard_form_text_filter()}</Label>
						<Input
							id="widget-text"
							maxlength={MAX_TEXT}
							bind:value={form.text}
							aria-invalid={error === 'text' || undefined}
						/>
					</div>
					<div class="flex flex-col gap-1">
						<Label for="widget-severity">{m.dashboard_form_severity()}</Label>
						<select id="widget-severity" class={selectClass} bind:value={form.minSeverity}>
							{#each severities as s (s.value)}<option value={s.value}>{s.label()}</option>{/each}
						</select>
					</div>
				</div>
			{/if}

			{#if error}
				<p role="alert" class="text-sm text-destructive">{errors[error]()}</p>
			{/if}
			<Dialog.Footer>
				<Button type="button" variant="outline" onclick={oncancel}
					>{m.dashboard_form_cancel()}</Button
				>
				<Button type="submit">{m.dashboard_form_apply()}</Button>
			</Dialog.Footer>
		</form>
	</Dialog.Content>
</Dialog.Root>
