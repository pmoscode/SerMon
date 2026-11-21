<script lang="ts">
	import { alertingApi } from '#lib/api/alerting.js';
	import {
		AlertRuleOrigin,
		type AlertRuleEntry
	} from '#lib/api/gen/sermon/web/v1/alert_rule_service_pb.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { notifyError } from '#lib/notify/notify.js';
	import { m } from '#lib/paraglide/messages.js';
	import { sessionState } from '#lib/stores/session.svelte.js';
	import { withStepUp } from '#lib/stores/stepup.svelte.js';
	import DiffView from './DiffView.svelte';
	import RuleEditor from './RuleEditor.svelte';

	/**
	 * The alert rules of the organization (M2-17): every member reads them with their help and
	 * YAML; admins switch them on and off, edit, import, export and restore the shipped rule.
	 */
	let list = $state<AlertRuleEntry[]>([]);
	let status = $state<'loading' | 'ready' | 'error'>('loading');
	let failure = $state('');
	let busy = $state(false);
	let search = $state('');
	let open = $state('');
	let editing = $state<{ title: string; yaml: string; file: boolean } | undefined>(undefined);
	let confirmReset = $state('');

	const admin = $derived(sessionState.current?.orgAdmin ?? false);

	async function load() {
		try {
			list = (await alertingApi.rules.listAlertRules({})).rules;
			status = 'ready';
		} catch {
			status = 'error';
		}
	}

	$effect(() => {
		void load();
	});

	async function run(action: () => Promise<unknown>) {
		busy = true;
		failure = '';
		try {
			await withStepUp(action);
			await load();
		} catch (error) {
			failure = notifyError(error);
		} finally {
			busy = false;
		}
	}

	const toggle = (r: AlertRuleEntry) =>
		run(() => alertingApi.rules.setAlertRuleEnabled({ ruleId: r.id, enabled: !r.enabled }));

	async function reset(r: AlertRuleEntry) {
		if (confirmReset !== r.id) {
			confirmReset = r.id;
			return;
		}
		confirmReset = '';
		await run(() => alertingApi.rules.resetAlertRule({ ruleId: r.id }));
	}

	async function exportRules(all: boolean) {
		failure = '';
		try {
			const res = await alertingApi.rules.exportAlertRules({ all });
			const url = URL.createObjectURL(new Blob([res.yaml], { type: 'text/yaml' }));
			const a = document.createElement('a');
			a.href = url;
			a.download = all ? 'sermon-rules-all.yaml' : 'sermon-rules.yaml';
			a.click();
			URL.revokeObjectURL(url);
		} catch (error) {
			failure = notifyError(error);
		}
	}

	const nameOf = (r: AlertRuleEntry) =>
		(localeState.current === 'de' ? r.name?.de : r.name?.en) || r.id;
	const helpOf = (r: AlertRuleEntry) =>
		(localeState.current === 'de' ? r.help?.de : r.help?.en) ?? '';
	const shown = $derived(
		list.filter((r) => {
			const q = search.trim().toLowerCase();
			return (
				!q || [r.id, r.name?.de, r.name?.en, r.labels.category].join(' ').toLowerCase().includes(q)
			);
		})
	);

	function originText(o: AlertRuleOrigin): string {
		switch (o) {
			case AlertRuleOrigin.CHANGED:
				return m.rules_origin_changed();
			case AlertRuleOrigin.OWN:
				return m.rules_origin_own();
			default:
				return m.rules_origin_default();
		}
	}

	const template = `apiVersion: sermon/v1
kind: AlertRule
metadata:
  id: custom.example.rule
  name: { de: "Beispiel", en: "Example" }
  labels: { category: custom }
spec:
  type: threshold
  metric: sermon_memory_utilization_ratio
  aggregate: avg
  window: 5m
  warning: { op: ">", value: 0.9 }
  help:
    de: "Was die Regel meldet und was zu tun ist."
    en: "What the rule reports and what to do."
`;
</script>

<div class="flex flex-col gap-4">
	<p class="text-sm text-muted-foreground">{m.rules_intro()}</p>
	{#if status === 'loading'}
		<p role="status" class="text-muted-foreground">{m.loading()}</p>
	{:else if status === 'error'}
		<div class="flex items-center gap-3">
			<p role="alert" class="text-destructive">{m.error_unavailable()}</p>
			<Button variant="outline" onclick={load}>{m.retry()}</Button>
		</div>
	{:else}
		{#if failure}
			<p role="alert" class="rounded-md border border-destructive/50 p-3 text-sm text-destructive">
				{failure}
			</p>
		{/if}
		{#if editing}
			<RuleEditor
				title={editing.title}
				initial={editing.yaml}
				file={editing.file}
				onDone={() => {
					editing = undefined;
					void load();
				}}
				onCancel={() => (editing = undefined)}
			/>
		{:else}
			<div class="flex flex-wrap items-end gap-2">
				<div class="flex flex-col gap-1">
					<Label for="rules-search">{m.rules_search()}</Label>
					<Input id="rules-search" type="search" class="w-64" bind:value={search} />
				</div>
				{#if admin}
					<Button
						variant="outline"
						onclick={() => (editing = { title: m.rules_new(), yaml: template, file: false })}
						>{m.rules_new()}</Button
					>
					<Button
						variant="outline"
						onclick={() => (editing = { title: m.rules_import(), yaml: '', file: true })}
						>{m.rules_import()}</Button
					>
				{/if}
				<Button variant="ghost" onclick={() => exportRules(false)}
					>{m.rules_export_changed()}</Button
				>
				<Button variant="ghost" onclick={() => exportRules(true)}>{m.rules_export_all()}</Button>
			</div>
		{/if}
		<ul class="divide-y rounded-lg border" aria-label={m.rules_title()}>
			{#each shown as r (r.id)}
				<li class="flex flex-col gap-2 px-3 py-3 text-sm">
					<div class="flex flex-wrap items-center justify-between gap-2">
						<span class="min-w-0">
							<span class="font-medium">{nameOf(r)}</span>
							<span class="rounded-md border px-1.5 py-0.5 text-xs">{originText(r.origin)}</span>
							{#if !r.enabled}<span class="rounded-md border px-1.5 py-0.5 text-xs"
									>{m.channel_disabled()}</span
								>{/if}
							<span class="block font-mono text-xs break-all text-muted-foreground"
								>{r.id} · {r.type}</span
							>
						</span>
						<span class="flex flex-wrap gap-1">
							<Button
								size="sm"
								variant="ghost"
								aria-expanded={open === r.id}
								onclick={() => (open = open === r.id ? '' : r.id)}
							>
								{m.rules_details()}
							</Button>
							{#if admin}
								<Button size="sm" variant="ghost" disabled={busy} onclick={() => toggle(r)}>
									{r.enabled ? m.channel_disable() : m.channel_enable()}
								</Button>
								<Button
									size="sm"
									variant="ghost"
									onclick={() =>
										(editing = { title: m.rules_edit({ rule: r.id }), yaml: r.yaml, file: false })}
								>
									{m.channel_edit()}
								</Button>
								{#if r.origin !== AlertRuleOrigin.DEFAULT}
									<Button size="sm" variant="ghost" disabled={busy} onclick={() => reset(r)}>
										{confirmReset === r.id
											? m.admin_delete_confirm()
											: r.origin === AlertRuleOrigin.OWN
												? m.admin_delete()
												: m.rules_reset()}
									</Button>
								{/if}
							{/if}
						</span>
					</div>
					{#if r.problem}
						<p role="alert" class="text-destructive">
							{m.rules_problem_stored({ message: r.problem })}
						</p>
					{/if}
					{#if open === r.id}
						<div class="flex flex-col gap-2">
							{#if helpOf(r)}<p class="whitespace-pre-line">{helpOf(r)}</p>{/if}
							{#if r.origin === AlertRuleOrigin.CHANGED}
								<DiffView
									before={r.defaultYaml}
									after={r.yaml}
									label={m.rules_diff_default({ rule: r.id })}
								/>
							{:else}
								<pre class="overflow-x-auto rounded-md border p-2 font-mono text-xs"><code
										>{r.yaml}</code
									></pre>
							{/if}
						</div>
					{/if}
				</li>
			{/each}
		</ul>
	{/if}
</div>
