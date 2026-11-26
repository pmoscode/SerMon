<script lang="ts">
	import { ConnectError } from '@connectrpc/connect';
	import LayoutDashboard from '@lucide/svelte/icons/layout-dashboard';
	import Plus from '@lucide/svelte/icons/plus';
	import Share2 from '@lucide/svelte/icons/share-2';
	import Upload from '@lucide/svelte/icons/upload';
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { dashboardsApi } from '#lib/api/dashboards.js';
	import type { Dashboard } from '#lib/api/gen/sermon/web/v1/dashboard_pb.js';
	import FleetNav from '#lib/components/fleet/FleetNav.svelte';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { newSpec } from '#lib/dashboards/model.js';
	import { displayText } from '#lib/logs/text.js';
	import { m } from '#lib/paraglide/messages.js';
	import { sessionState } from '#lib/stores/session.svelte.js';

	/** The custom dashboards of the member (KONZEPT §16, M5-24): own and shared ones. */
	const MAX_IMPORT = 32 * 1024;

	let dashboards = $state.raw<Dashboard[]>([]);
	let status = $state<'loading' | 'ready' | 'error'>('loading');
	let title = $state('');
	let busy = $state(false);
	let failure = $state('');
	let file = $state<HTMLInputElement>();

	async function load() {
		status = 'loading';
		try {
			dashboards = (await dashboardsApi.listDashboards({})).dashboards;
			status = 'ready';
		} catch {
			status = 'error';
		}
	}
	void load();

	function open(id: string, edit = false) {
		const path = resolve('/(app)/dashboards/[id]', { id });
		// eslint-disable-next-line svelte/no-navigation-without-resolve -- resolve() with the editor's query
		return goto(edit ? `${path}?edit=1` : path);
	}

	function reason(error: unknown): string {
		return ConnectError.from(error).rawMessage;
	}

	async function create(event: SubmitEvent) {
		event.preventDefault();
		if (!title.trim()) return;
		busy = true;
		failure = '';
		try {
			const res = await dashboardsApi.createDashboard({ spec: newSpec(title.trim()) });
			if (res.dashboard) await open(res.dashboard.id, true);
		} catch (error) {
			failure = m.dashboards_create_failed({ reason: reason(error) });
		} finally {
			busy = false;
		}
	}

	async function importFile() {
		const chosen = file?.files?.[0];
		if (!chosen) return;
		failure = '';
		if (chosen.size > MAX_IMPORT) {
			failure = m.dashboards_import_too_large();
			return;
		}
		busy = true;
		try {
			const res = await dashboardsApi.importDashboard({ json: await chosen.text() });
			if (res.dashboard) await open(res.dashboard.id);
		} catch (error) {
			failure = m.dashboards_import_failed({ reason: reason(error) });
		} finally {
			busy = false;
			if (file) file.value = '';
		}
	}

	const me = $derived(sessionState.current?.userId);
</script>

<svelte:head>
	<title>{m.dashboards_title()} · SerMon</title>
</svelte:head>

<FleetNav />

<div class="flex flex-col gap-4 pt-4">
	<div class="flex flex-wrap items-baseline gap-2">
		<h1 class="text-2xl font-semibold tracking-tight">{m.dashboards_title()}</h1>
		<HelpTip topic="pages/dashboards" />
	</div>
	<p class="max-w-3xl text-sm text-muted-foreground">{m.dashboards_intro()}</p>

	<div class="flex flex-wrap items-end gap-3">
		<form class="flex flex-wrap items-end gap-2" onsubmit={create}>
			<div class="flex flex-col gap-1">
				<Label for="dashboard-title">{m.dashboards_new_title()}</Label>
				<Input id="dashboard-title" class="w-64" maxlength={200} bind:value={title} required />
			</div>
			<Button type="submit" disabled={busy || !title.trim()}>
				<Plus aria-hidden="true" />
				{m.dashboards_create()}
			</Button>
		</form>
		<input
			bind:this={file}
			type="file"
			accept="application/json,.json"
			class="sr-only"
			id="dashboard-import"
			aria-label={m.dashboards_import()}
			onchange={importFile}
		/>
		<Button variant="outline" disabled={busy} onclick={() => file?.click()}>
			<Upload aria-hidden="true" />
			{m.dashboards_import()}
		</Button>
	</div>
	{#if failure}
		<p role="alert" class="text-sm text-destructive">{failure}</p>
	{/if}

	{#if status === 'loading'}
		<p role="status" class="text-muted-foreground">{m.loading()}</p>
	{:else if status === 'error'}
		<div class="flex flex-col items-start gap-2">
			<p role="alert" class="text-destructive">{m.error_unavailable()}</p>
			<Button variant="outline" onclick={load}>{m.retry()}</Button>
		</div>
	{:else if dashboards.length === 0}
		<div class="flex flex-col items-start gap-2 rounded-lg border border-dashed p-6">
			<LayoutDashboard class="size-6 text-muted-foreground" aria-hidden="true" />
			<p class="font-medium">{m.dashboards_empty()}</p>
			<p class="text-sm text-muted-foreground">{m.dashboards_empty_hint()}</p>
		</div>
	{:else}
		<ul class="flex flex-col divide-y rounded-lg border" aria-label={m.dashboards_list()}>
			{#each dashboards as d (d.id)}
				<li class="flex flex-wrap items-center gap-x-3 gap-y-1 px-4 py-3">
					<a
						href={resolve('/(app)/dashboards/[id]', { id: d.id })}
						class="font-medium hover:underline focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
					>
						{displayText(d.spec?.title ?? '')}
					</a>
					{#if d.shared}
						<span class="inline-flex items-center gap-1 rounded-full border px-2 text-xs">
							<Share2 class="size-3" aria-hidden="true" />
							{m.dashboards_shared()}
						</span>
					{/if}
					<span class="text-sm text-muted-foreground">
						{d.ownerId === me
							? m.dashboards_owner_you()
							: m.dashboards_owner({
									name: displayText(d.ownerName || m.dashboards_owner_former())
								})}
					</span>
					{#if d.spec?.description}
						<span class="w-full truncate text-sm text-muted-foreground"
							>{displayText(d.spec.description)}</span
						>
					{/if}
				</li>
			{/each}
		</ul>
	{/if}
</div>
