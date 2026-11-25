<script lang="ts">
	import { untrack } from 'svelte';
	import type { Weekday } from '#lib/api/gen/sermon/web/v1/silence_pb.js';
	import { UpdateMode, type UpdatePolicy } from '#lib/api/gen/sermon/web/v1/update_policy_pb.js';
	import { updatesApi } from '#lib/api/updates.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { minutesOfTime, timeOfMinutes, weekdays } from '#lib/notify/forms.js';
	import { notifyError } from '#lib/notify/notify.js';
	import { m } from '#lib/paraglide/messages.js';
	import { withStepUp } from '#lib/stores/stepup.svelte.js';
	import { modeLabel } from '#lib/updates/labels.js';

	/**
	 * The update policy of a group (M4-10, M4-15): mode, window, reboot and rollout as a form, or as
	 * the YAML of KONZEPT §14.2. Saving needs an admin of all groups and a step-up; the server checks
	 * everything again.
	 */
	let { policy, onsaved }: { policy: UpdatePolicy; onsaved: () => void } = $props();

	const browserZone = Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
	// The form starts from the stored policy; a new policy comes with a new editor.
	const spec = untrack(() => policy.spec);
	let mode = $state<UpdateMode>(spec?.mode ?? UpdateMode.REPORT);
	let days = $state<Weekday[]>(spec?.window?.days ?? []);
	let start = $state(timeOfMinutes(spec?.window?.startMinute ?? 180));
	let duration = $state(spec?.window?.durationMinutes ?? 120);
	let zone = $state(spec?.window?.timeZone ?? browserZone);
	let autoReboot = $state(spec?.autoReboot ?? false);
	let parallel = $state(spec?.maxParallel ?? 1);
	let stop = $state(spec?.stopOnFailure ?? true);
	let health = $state(spec?.healthCheckMinutes ?? 10);
	let yaml = $state('');
	let busy = $state(false);
	let failure = $state('');
	let done = $state('');

	const windowed = $derived(mode !== UpdateMode.REPORT || autoReboot);
	const complete = $derived(
		(!windowed || (days.length > 0 && minutesOfTime(start) !== undefined)) &&
			duration >= 15 &&
			duration <= 1440 &&
			parallel >= 1 &&
			parallel <= 50 &&
			health >= 5 &&
			health <= 120
	);

	async function act(action: () => Promise<unknown>) {
		busy = true;
		failure = '';
		done = '';
		try {
			await withStepUp(action);
			done = m.policy_saved();
			onsaved();
		} catch (error) {
			failure = notifyError(error);
		} finally {
			busy = false;
		}
	}

	function save(event: SubmitEvent) {
		event.preventDefault();
		if (!complete) return;
		return act(() =>
			updatesApi.policies.setUpdatePolicy({
				groupId: policy.groupId,
				spec: {
					mode,
					autoReboot,
					maxParallel: parallel,
					stopOnFailure: stop,
					healthCheckMinutes: health,
					window:
						days.length > 0
							? {
									days,
									startMinute: minutesOfTime(start) ?? 0,
									durationMinutes: duration,
									timeZone: zone
								}
							: undefined
				}
			})
		);
	}

	async function exportYaml() {
		try {
			yaml = (await updatesApi.policies.exportUpdatePolicy({ groupId: policy.groupId })).yaml;
		} catch (error) {
			failure = notifyError(error);
		}
	}

	function toggle(day: Weekday, on: boolean) {
		days = on ? [...new Set([...days, day])] : days.filter((d) => d !== day);
	}
</script>

<form
	class="flex flex-col gap-4 rounded-lg border p-4"
	onsubmit={save}
	aria-label={m.policy_title({ group: policy.groupName })}
>
	<fieldset class="flex flex-col gap-1 text-sm">
		<legend class="font-medium">{m.policy_mode()}</legend>
		{#each [UpdateMode.REPORT, UpdateMode.SECURITY_AUTO, UpdateMode.ALL_AUTO] as value (value)}
			<label class="flex items-center gap-2">
				<input type="radio" bind:group={mode} {value} />
				{modeLabel(value)}
			</label>
		{/each}
	</fieldset>
	{#if mode === UpdateMode.SECURITY_AUTO}
		<p class="text-sm text-muted-foreground" role="note">{m.policy_alpine()}</p>
	{/if}
	<fieldset class="flex flex-wrap gap-3 rounded-md border p-3 text-sm">
		<legend class="px-1 font-medium">{m.policy_days()}</legend>
		{#each weekdays as d (d.day)}
			<label class="flex items-center gap-1">
				<input
					type="checkbox"
					class="size-4"
					checked={days.includes(d.day)}
					onchange={(e) => toggle(d.day, e.currentTarget.checked)}
				/>
				{d.name()}
			</label>
		{/each}
	</fieldset>
	<div class="flex flex-wrap items-end gap-3">
		<div class="flex flex-col gap-1">
			<Label for="policy-start">{m.policy_start()}</Label>
			<Input id="policy-start" type="time" class="w-32" bind:value={start} />
		</div>
		<div class="flex flex-col gap-1">
			<Label for="policy-duration">{m.policy_duration()}</Label>
			<Input
				id="policy-duration"
				type="number"
				min="15"
				max="1440"
				class="w-32"
				bind:value={duration}
			/>
		</div>
		<div class="flex flex-col gap-1">
			<Label for="policy-zone">{m.policy_timezone()}</Label>
			<Input id="policy-zone" class="w-56" maxlength={64} bind:value={zone} />
		</div>
	</div>
	<div class="flex flex-wrap items-end gap-3">
		<div class="flex flex-col gap-1">
			<Label for="policy-parallel">{m.policy_parallel()}</Label>
			<Input
				id="policy-parallel"
				type="number"
				min="1"
				max="50"
				class="w-32"
				bind:value={parallel}
			/>
		</div>
		<div class="flex flex-col gap-1">
			<Label for="policy-health">{m.policy_health()}</Label>
			<Input id="policy-health" type="number" min="5" max="120" class="w-32" bind:value={health} />
		</div>
		<label class="flex h-9 items-center gap-2 text-sm">
			<input type="checkbox" class="size-4" bind:checked={stop} />
			{m.policy_stop()}
		</label>
		<label class="flex h-9 items-center gap-2 text-sm">
			<input type="checkbox" class="size-4" bind:checked={autoReboot} />
			{m.policy_auto_reboot()}
		</label>
	</div>
	{#if windowed && days.length === 0}
		<p class="text-sm" role="note">{m.policy_need_window()}</p>
	{/if}
	<div class="flex flex-wrap gap-2">
		<Button type="submit" size="sm" disabled={busy || !complete || (windowed && days.length === 0)}
			>{m.policy_save()}</Button
		>
		<Button
			type="button"
			size="sm"
			variant="outline"
			disabled={busy}
			onclick={() => act(() => updatesApi.policies.resetUpdatePolicy({ groupId: policy.groupId }))}
			>{m.policy_reset()}</Button
		>
		<Button type="button" size="sm" variant="ghost" onclick={exportYaml}>{m.policy_yaml()}</Button>
	</div>
	{#if yaml}
		<div class="flex flex-col gap-1">
			<Label for="policy-yaml">{m.policy_yaml()}</Label>
			<textarea
				id="policy-yaml"
				class="min-h-40 rounded-md border bg-background p-2 font-mono text-xs"
				maxlength={8192}
				bind:value={yaml}></textarea>
			<Button
				type="button"
				size="sm"
				variant="outline"
				class="self-start"
				disabled={busy || yaml.trim() === ''}
				onclick={() =>
					act(() => updatesApi.policies.importUpdatePolicy({ groupId: policy.groupId, yaml }))}
				>{m.policy_import()}</Button
			>
		</div>
	{/if}
	{#if done}<p class="text-sm" aria-live="polite">{done}</p>{/if}
	{#if failure}<p role="alert" class="text-sm text-destructive">{failure}</p>{/if}
</form>
