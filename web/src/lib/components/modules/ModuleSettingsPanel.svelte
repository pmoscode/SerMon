<script lang="ts">
	import { modulesApi } from '#lib/api/modules.js';
	import type {
		GetModuleSettingsResponse,
		GroupModuleSettings,
		Module,
		ModuleSetting
	} from '#lib/api/gen/sermon/web/v1/module_pb.js';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import { moduleLabel } from '#lib/modules/labels.js';
	import type { Saved } from '#lib/modules/schema.js';
	import { modulesError } from '#lib/modules/text.js';
	import { m } from '#lib/paraglide/messages.js';
	import { needsStepUp, withStepUp } from '#lib/stores/stepup.svelte.js';
	import ModuleForm from './ModuleForm.svelte';

	/**
	 * The modules of every node group (ADR 0039, M5-07): which run and how. A group offers the
	 * modules its nodes' agents have built in. Every change asks for a recent re-authentication.
	 */
	let {
		opening
	}: {
		/** A module to open at once, e.g. from "nginx detected – enable the module?". */
		opening?: { group: string; module: string; suggestion?: string };
	} = $props();

	let data = $state.raw<GetModuleSettingsResponse | undefined>(undefined);
	let catalog = $state.raw<Module[]>([]);
	let status = $state<'loading' | 'ready' | 'error'>('loading');
	let failure = $state('');
	// svelte-ignore state_referenced_locally
	let editing = $state<{ group: string; module: string; suggestion?: string } | undefined>(opening);
	let busy = $state(false);

	async function load() {
		try {
			const [settings, list] = await Promise.all([
				modulesApi.modules.getModuleSettings({}),
				modulesApi.modules.listModules({})
			]);
			data = settings;
			catalog = list.modules;
			status = 'ready';
		} catch (error) {
			failure = modulesError(error);
			status = 'error';
		}
	}

	$effect(() => {
		void load();
	});

	async function change(run: () => Promise<unknown>): Promise<string> {
		try {
			await withStepUp(run);
			await load();
			return '';
		} catch (error) {
			if (needsStepUp(error)) return '';
			return modulesError(error);
		}
	}

	async function save(group: GroupModuleSettings, module: string, saved: Saved) {
		const message = await change(() =>
			modulesApi.modules.setGroupModule({
				groupId: group.groupId,
				setting: {
					moduleId: module,
					enabled: saved.enabled,
					configJson: saved.configJson,
					interval:
						saved.intervalSeconds === undefined
							? undefined
							: { seconds: BigInt(saved.intervalSeconds) }
				},
				secrets: saved.secrets,
				clearSecrets: saved.clearSecrets
			})
		);
		if (message === '') editing = undefined;
		return message;
	}

	async function showAgain(group: GroupModuleSettings, module: string) {
		busy = true;
		failure = await change(() =>
			modulesApi.modules.setModuleHintDismissed({
				groupId: group.groupId,
				moduleId: module,
				dismissed: false
			})
		);
		busy = false;
	}

	async function remove(group: GroupModuleSettings, module: string) {
		busy = true;
		failure = await change(() =>
			modulesApi.modules.removeGroupModule({ groupId: group.groupId, moduleId: module })
		);
		busy = false;
	}

	/** The modules a group offers: those set up and those its agents have built in. */
	function offered(g: GroupModuleSettings): string[] {
		const known = new Set(catalog.map((c) => c.id));
		const ids = new Set([...g.modules.map((s) => s.moduleId), ...g.available]);
		return [...ids].filter((id) => known.has(id)).sort();
	}

	const schemaOf = (id: string) => catalog.find((c) => c.id === id);

	function summary(setting: ModuleSetting | undefined): string {
		if (!setting) return m.modules_not_set();
		const parts = [setting.enabled ? m.modules_on() : m.modules_off()];
		if (setting.interval)
			parts.push(m.modules_interval_value({ seconds: Number(setting.interval.seconds) }));
		return parts.join(' · ');
	}
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
			<p class="text-sm text-muted-foreground">{m.modules_intro()}</p>
			<HelpTip topic="concepts/modules" />
		</div>
		{#if failure}
			<p role="alert" class="rounded-md border border-destructive/50 p-3 text-sm text-destructive">
				{failure}
			</p>
		{/if}
		{#each data.groups as group (group.groupId)}
			{@const ids = offered(group)}
			<section class="flex flex-col gap-2" aria-labelledby="modules-{group.groupId}">
				<h2 id="modules-{group.groupId}" class="text-lg font-medium">{group.groupName}</h2>
				{#if ids.length === 0}
					<p class="text-sm text-muted-foreground">{m.modules_group_none()}</p>
				{:else}
					<ul class="flex flex-col divide-y divide-border rounded-md border border-border">
						{#each ids as id (id)}
							{@const setting = group.modules.find((s) => s.moduleId === id)}
							{@const module = schemaOf(id)}
							<li class="flex flex-col gap-2 p-3">
								<div class="flex flex-wrap items-center justify-between gap-2">
									<div class="flex flex-col">
										<span class="flex items-center gap-1 font-medium"
											>{moduleLabel(id)}<HelpTip topic={`modules/${id}`} /></span
										>
										<span class="text-sm text-muted-foreground">{summary(setting)}</span>
									</div>
									<div class="flex gap-2">
										<Button
											variant="outline"
											size="sm"
											aria-label="{m.modules_edit()}: {moduleLabel(id)} ({group.groupName})"
											onclick={() => (editing = { group: group.groupId, module: id })}
											>{m.modules_edit()}</Button
										>
										{#if setting}
											<Button
												variant="ghost"
												size="sm"
												disabled={busy}
												aria-label="{m.modules_remove()}: {moduleLabel(id)} ({group.groupName})"
												onclick={() => remove(group, id)}>{m.modules_remove()}</Button
											>
										{/if}
									</div>
								</div>
								{#if module && editing?.group === group.groupId && editing.module === id}
									<ModuleForm
										moduleId={id}
										schemaJson={module.schemaJson}
										{setting}
										secretsSet={setting?.secretsSet ?? []}
										suggestion={editing.suggestion}
										onSave={(saved) => save(group, id, saved)}
										onCancel={() => (editing = undefined)}
									/>
								{/if}
							</li>
						{/each}
					</ul>
				{/if}
				{#if group.dismissedHints.length > 0}
					<div class="flex flex-col gap-1" data-testid="dismissed-hints">
						<h3 class="text-sm font-medium">{m.modules_dismissed_title()}</h3>
						<p class="text-sm text-muted-foreground">{m.modules_dismissed_help()}</p>
						<ul class="flex flex-col gap-1">
							{#each group.dismissedHints as id (id)}
								<li class="flex flex-wrap items-center gap-2 text-sm">
									<span>{moduleLabel(id)}</span>
									<Button
										variant="outline"
										size="sm"
										disabled={busy}
										aria-label="{m.modules_dismissed_show()}: {moduleLabel(id)} ({group.groupName})"
										onclick={() => showAgain(group, id)}>{m.modules_dismissed_show()}</Button
									>
								</li>
							{/each}
						</ul>
					</div>
				{/if}
			</section>
		{/each}
	</div>
{/if}
