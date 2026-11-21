<script lang="ts">
	import { api } from '#lib/api/client.js';
	import { alertingApi } from '#lib/api/alerting.js';
	import type { NodeGroup } from '#lib/api/gen/sermon/web/v1/access_pb.js';
	import type { MaintenanceWindow, Weekday } from '#lib/api/gen/sermon/web/v1/silence_pb.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { durationText, minutesOfTime, timeOfMinutes, weekdays } from '#lib/notify/forms.js';
	import { notifyError } from '#lib/notify/notify.js';
	import { m } from '#lib/paraglide/messages.js';
	import { withStepUp } from '#lib/stores/stepup.svelte.js';

	/** Weekly maintenance windows (M2-15): they mute reboot and offline alerts of their groups. */
	let windows = $state<MaintenanceWindow[]>([]);
	let groups = $state<NodeGroup[]>([]);
	let status = $state<'loading' | 'ready' | 'error'>('loading');
	let failure = $state('');
	let busy = $state(false);
	let editing = $state<MaintenanceWindow | 'new' | undefined>(undefined);
	let confirmDelete = $state('');

	const browserZone = Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
	const zones = (() => {
		try {
			return Intl.supportedValuesOf('timeZone');
		} catch {
			return [browserZone, 'UTC'];
		}
	})();

	// The form.
	let name = $state('');
	let enabled = $state(true);
	let groupIds = $state<string[]>([]);
	let days = $state<Weekday[]>([]);
	let start = $state('03:00');
	let duration = $state(120);
	let zone = $state(browserZone);

	function edit(w: MaintenanceWindow | 'new') {
		const s = w === 'new' ? undefined : w.spec;
		name = s?.name ?? '';
		enabled = s?.enabled ?? true;
		groupIds = [...(s?.groupIds ?? [])];
		days = [...(s?.days ?? [])];
		start = timeOfMinutes(s?.startMinute ?? 180);
		duration = s?.durationMinutes ?? 120;
		zone = s?.timeZone ?? browserZone;
		editing = w;
	}

	async function load() {
		try {
			const [w, g] = await Promise.all([
				alertingApi.windows.listMaintenanceWindows({}),
				api.access.listNodeGroups({})
			]);
			windows = w.windows;
			groups = g.groups;
			status = 'ready';
		} catch {
			status = 'error';
		}
	}

	$effect(() => {
		void load();
	});

	async function run(action: () => Promise<unknown>): Promise<boolean> {
		busy = true;
		failure = '';
		try {
			await withStepUp(action);
			await load();
			return true;
		} catch (error) {
			failure = notifyError(error);
			return false;
		} finally {
			busy = false;
		}
	}

	async function save(event: SubmitEvent) {
		event.preventDefault();
		const spec = {
			name: name.trim(),
			enabled,
			groupIds,
			days,
			startMinute: minutesOfTime(start) ?? 0,
			durationMinutes: Number(duration),
			timeZone: zone
		};
		const current = editing;
		const ok = await run(() =>
			current === 'new' || !current
				? alertingApi.windows.createMaintenanceWindow({ spec })
				: alertingApi.windows.updateMaintenanceWindow({ windowId: current.id, spec })
		);
		if (ok) editing = undefined;
	}

	async function remove(w: MaintenanceWindow) {
		if (confirmDelete !== w.id) {
			confirmDelete = w.id;
			return;
		}
		confirmDelete = '';
		await run(() => alertingApi.windows.deleteMaintenanceWindow({ windowId: w.id }));
	}

	function toggle<T>(list: T[], value: T, on: boolean): T[] {
		return on ? [...new Set([...list, value])] : list.filter((v) => v !== value);
	}

	const dayNames = (list: Weekday[]) =>
		weekdays
			.filter((d) => list.includes(d.day))
			.map((d) => d.name())
			.join(', ');
	const groupNames = (ids: string[]) =>
		ids.length
			? ids.map((id) => groups.find((g) => g.id === id)?.name ?? '–').join(', ')
			: m.admin_all_groups();
	const complete = $derived(
		name.trim() !== '' &&
			days.length > 0 &&
			minutesOfTime(start) !== undefined &&
			duration >= 15 &&
			duration <= 1440
	);
</script>

<div class="flex flex-col gap-4">
	<p class="text-sm text-muted-foreground">{m.windows_intro()}</p>
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
			<form
				class="flex flex-col gap-4 rounded-lg border p-4"
				onsubmit={save}
				aria-label={editing === 'new' ? m.window_new() : m.window_edit()}
			>
				<div class="flex flex-wrap items-end gap-3">
					<div class="flex flex-col gap-1">
						<Label for="window-name">{m.window_name()}</Label>
						<Input id="window-name" class="w-64" maxlength={100} bind:value={name} />
					</div>
					<div class="flex flex-col gap-1">
						<Label for="window-start">{m.window_start()}</Label>
						<Input id="window-start" type="time" class="w-32" bind:value={start} />
					</div>
					<div class="flex flex-col gap-1">
						<Label for="window-duration">{m.window_duration()}</Label>
						<Input
							id="window-duration"
							type="number"
							min="15"
							max="1440"
							class="w-32"
							bind:value={duration}
						/>
					</div>
					<div class="flex flex-col gap-1">
						<Label for="window-zone">{m.window_zone()}</Label>
						<select
							id="window-zone"
							class="h-9 w-56 rounded-md border border-input bg-background px-2 text-sm focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
							bind:value={zone}
						>
							{#each zones as z (z)}<option value={z}>{z}</option>{/each}
						</select>
					</div>
					<label class="flex h-9 items-center gap-2 text-sm">
						<input type="checkbox" class="size-4" bind:checked={enabled} />
						{m.window_enabled()}
					</label>
				</div>
				<fieldset class="flex flex-wrap gap-3 rounded-md border p-3 text-sm">
					<legend class="px-1 font-medium">{m.window_days()}</legend>
					{#each weekdays as d (d.day)}
						<label class="flex items-center gap-1">
							<input
								type="checkbox"
								class="size-4"
								checked={days.includes(d.day)}
								onchange={(e) => (days = toggle(days, d.day, e.currentTarget.checked))}
							/>
							{d.name()}
						</label>
					{/each}
				</fieldset>
				<fieldset class="flex flex-wrap gap-3 rounded-md border p-3 text-sm">
					<legend class="px-1 font-medium">{m.window_groups()}</legend>
					{#each groups as g (g.id)}
						<label class="flex items-center gap-1">
							<input
								type="checkbox"
								class="size-4"
								checked={groupIds.includes(g.id)}
								onchange={(e) => (groupIds = toggle(groupIds, g.id, e.currentTarget.checked))}
							/>
							{g.name}
						</label>
					{/each}
					<p class="w-full text-xs text-muted-foreground">{m.window_groups_hint()}</p>
				</fieldset>
				<div class="flex gap-2">
					<Button type="submit" disabled={busy || !complete}
						>{editing === 'new' ? m.window_create() : m.admin_save()}</Button
					>
					<Button type="button" variant="ghost" onclick={() => (editing = undefined)}
						>{m.notify_cancel()}</Button
					>
				</div>
			</form>
		{:else}
			<Button class="self-start" onclick={() => edit('new')}>{m.window_new()}</Button>
		{/if}
		<ul class="divide-y rounded-lg border" aria-label={m.windows_title()}>
			{#each windows as w (w.id)}
				{@const s = w.spec}
				<li class="flex flex-wrap items-center justify-between gap-2 px-3 py-3 text-sm">
					<span>
						<span class="font-medium">{s?.name}</span>
						{#if !s?.enabled}<span class="ml-1 rounded-md border px-1.5 py-0.5 text-xs"
								>{m.channel_disabled()}</span
							>{/if}
						<span class="block text-xs text-muted-foreground">
							{m.window_summary({
								days: dayNames(s?.days ?? []),
								start: timeOfMinutes(s?.startMinute ?? 0),
								duration: durationText(s?.durationMinutes ?? 0),
								zone: s?.timeZone ?? '',
								groups: groupNames(s?.groupIds ?? [])
							})}
						</span>
					</span>
					<span class="flex gap-1">
						<Button size="sm" variant="ghost" onclick={() => edit(w)}>{m.channel_edit()}</Button>
						<Button size="sm" variant="ghost" disabled={busy} onclick={() => remove(w)}>
							{confirmDelete === w.id ? m.admin_delete_confirm() : m.admin_delete()}
						</Button>
					</span>
				</li>
			{:else}
				<li class="px-3 py-4 text-sm text-muted-foreground">{m.windows_none()}</li>
			{/each}
		</ul>
	{/if}
</div>
