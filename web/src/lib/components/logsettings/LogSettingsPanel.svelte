<script lang="ts">
	import type { MessageInitShape } from '@bufbuild/protobuf';
	import { adminApi } from '#lib/api/admin.js';
	import type {
		GetLogSettingsResponse,
		GroupLogSettings,
		LogSettingsSchema
	} from '#lib/api/gen/sermon/web/v1/log_settings_pb.js';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import { severities } from '#lib/logs/filter.js';
	import { severityNameLabel } from '#lib/logs/labels.js';
	import { logSettingsError } from '#lib/logsettings/errors.js';
	import { needsStepUp, withStepUp } from '#lib/stores/stepup.svelte.js';
	import { m } from '#lib/paraglide/messages.js';
	import GroupLogSettingsForm from './GroupLogSettingsForm.svelte';
	import NodeConfigTable from './NodeConfigTable.svelte';
	import RedactionRulesEditor from './RedactionRulesEditor.svelte';

	/**
	 * The log settings of the organization (KONZEPT §10, M3-12): who sees logs, what the agents of
	 * each group send, the redaction rules and what each agent made of it. Every change asks for a
	 * recent re-authentication.
	 */
	let data = $state.raw<GetLogSettingsResponse | undefined>(undefined);
	let status = $state<'loading' | 'ready' | 'error'>('loading');
	let failure = $state('');
	let editing = $state<string | undefined>(undefined);
	let busy = $state(false);
	let now = $state(new Date());

	async function load() {
		try {
			data = await adminApi.logSettings.getLogSettings({});
			now = new Date();
			status = 'ready';
		} catch (error) {
			failure = logSettingsError(error);
			status = 'error';
		}
	}

	$effect(() => {
		void load();
	});

	/** Runs a change with a step-up; the message of a failure, empty on success. */
	async function change(run: () => Promise<unknown>): Promise<string> {
		try {
			await withStepUp(run);
			await load();
			return '';
		} catch (error) {
			if (needsStepUp(error)) return '';
			return logSettingsError(error);
		}
	}

	async function setViewers(see: boolean) {
		busy = true;
		failure = await change(() => adminApi.logSettings.setViewerLogAccess({ viewersSeeLogs: see }));
		busy = false;
	}

	async function saveGroup(
		group: GroupLogSettings,
		settings: MessageInitShape<typeof LogSettingsSchema>
	) {
		const message = await change(() =>
			adminApi.logSettings.setGroupLogSettings({ groupId: group.groupId, settings })
		);
		if (message === '') editing = undefined;
		return message;
	}

	async function reset(group: GroupLogSettings) {
		busy = true;
		failure = await change(() =>
			adminApi.logSettings.resetGroupLogSettings({ groupId: group.groupId })
		);
		busy = false;
	}

	function summary(g: GroupLogSettings): string {
		const s = g.settings;
		if (!s?.enabled) return m.logsettings_summary_off();
		const parts = [
			s.minSeverity > 0 && s.minSeverity <= severities.length
				? m.logs_filter_at_least({ severity: severityNameLabel(severities[s.minSeverity - 1]) })
				: m.logsettings_all_severities()
		];
		if (s.excludeUnits.length)
			parts.push(m.logsettings_summary_units({ count: s.excludeUnits.length }));
		if (s.files.length) parts.push(m.logsettings_summary_files({ count: s.files.length }));
		return parts.join(' · ');
	}

	const groupNames = $derived(new Map((data?.groups ?? []).map((g) => [g.groupId, g.groupName])));
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
	<div class="flex max-w-4xl flex-col gap-8">
		<div class="flex items-center gap-2">
			<p class="text-sm text-muted-foreground">{m.logsettings_intro()}</p>
			<HelpTip topic="concepts/log_settings" />
		</div>
		{#if failure}
			<p role="alert" class="rounded-md border border-destructive/50 p-3 text-sm text-destructive">
				{failure}
			</p>
		{/if}

		<section class="flex flex-col gap-2" aria-labelledby="viewer-logs-title">
			<h2 id="viewer-logs-title" class="text-lg font-medium">{m.logsettings_viewers_title()}</h2>
			<p class="text-sm text-muted-foreground">{m.logsettings_viewers_text()}</p>
			<p class="text-sm" role="status">
				{data.viewersSeeLogs ? m.logsettings_viewers_on() : m.logsettings_viewers_off()}
			</p>
			<Button
				class="self-start"
				variant="outline"
				size="sm"
				disabled={busy}
				onclick={() => setViewers(!data!.viewersSeeLogs)}
			>
				{data.viewersSeeLogs ? m.logsettings_viewers_disable() : m.logsettings_viewers_enable()}
			</Button>
		</section>

		<section class="flex flex-col gap-2" aria-labelledby="group-logs-title">
			<h2 id="group-logs-title" class="text-lg font-medium">{m.logsettings_groups_title()}</h2>
			<p class="text-sm text-muted-foreground">{m.logsettings_groups_text()}</p>
			<ul class="flex flex-col gap-2">
				{#each data.groups as g (g.groupId)}
					<li class="flex flex-col gap-2">
						<div class="flex flex-wrap items-center gap-2 rounded-md border border-border p-3">
							<span class="font-medium">{g.groupName}</span>
							<span class="text-sm text-muted-foreground">{summary(g)}</span>
							<span class="text-xs text-muted-foreground">
								{g.custom ? m.logsettings_custom() : m.logsettings_default()}
							</span>
							<div class="ml-auto flex gap-2">
								<Button
									variant="outline"
									size="sm"
									aria-expanded={editing === g.groupId}
									aria-label={m.logsettings_edit({ group: g.groupName })}
									onclick={() => (editing = editing === g.groupId ? undefined : g.groupId)}
								>
									{m.logsettings_edit_short()}
								</Button>
								{#if g.custom}
									<Button
										variant="ghost"
										size="sm"
										disabled={busy}
										aria-label={m.logsettings_reset({ group: g.groupName })}
										onclick={() => reset(g)}
									>
										{m.logsettings_reset_short()}
									</Button>
								{/if}
							</div>
						</div>
						{#if editing === g.groupId}
							<GroupLogSettingsForm
								group={g}
								onSave={(s) => saveGroup(g, s)}
								onCancel={() => (editing = undefined)}
							/>
						{/if}
					</li>
				{/each}
			</ul>
		</section>

		{#key data.version}
			<RedactionRulesEditor
				rules={data.redactionRules.map((r) => r.pattern)}
				onSave={(rules) =>
					change(() =>
						adminApi.logSettings.setRedactionRules({ rules: rules.map((pattern) => ({ pattern })) })
					)}
			/>
		{/key}

		<NodeConfigTable nodes={data.nodes} version={data.version} {groupNames} {now} />
	</div>
{/if}
