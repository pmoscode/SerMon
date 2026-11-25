<script lang="ts">
	import Pause from '@lucide/svelte/icons/pause';
	import Play from '@lucide/svelte/icons/play';
	import { timestampDate } from '@bufbuild/protobuf/wkt';
	import { untrack } from 'svelte';
	import type { ProcessInfo } from '#lib/api/gen/sermon/web/v1/live_view_pb.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { formatNumber } from '#lib/format/format.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { displayText } from '#lib/logs/text.js';
	import { m } from '#lib/paraglide/messages.js';
	import { ProcessViewState } from '#lib/processes/live.svelte.js';
	import {
		compileRegex,
		DEFAULT_SORT,
		MAX_REGEX,
		nextSort,
		processRows,
		users,
		type Sort
	} from '#lib/processes/table.js';
	import ProcessSummary from './ProcessSummary.svelte';
	import ProcessTable from './ProcessTable.svelte';

	/**
	 * The live process view of a node (KONZEPT §9, M3-15): the header like top's, the processes
	 * sorted, filtered in the browser or as a tree, a pause and the details of a process. The
	 * stream runs while the view is open; it never acts on a process (D19).
	 */
	let { nodeId }: { nodeId: string } = $props();

	const view = new ProcessViewState();
	let sort = $state<Sort>(DEFAULT_SORT);
	let user = $state('');
	let name = $state('');
	let regexSource = $state('');
	let tree = $state(false);
	let selectedPid = $state<number | undefined>(undefined);
	/** The last state of the selected process, kept when it ends. */
	let lastSelected = $state.raw<ProcessInfo | undefined>(undefined);

	$effect(() => {
		const id = nodeId;
		untrack(() => view.start(id));
		return () => view.stop();
	});

	const locale = $derived(localeState.current);
	const processes = $derived(view.snapshot?.processes ?? []);
	const byPid = $derived(new Map(processes.map((p) => [p.pid, p])));
	const regex = $derived(compileRegex(regexSource));
	const rows = $derived(
		processRows(processes, {
			sort,
			filter: { user, name, regex: regexSource },
			regex: regex.regex,
			tree
		})
	);
	const userNames = $derived(users(processes));
	const current = $derived(selectedPid === undefined ? undefined : byPid.get(selectedPid));
	const shownProcess = $derived(selectedPid === undefined ? undefined : (current ?? lastSelected));

	$effect(() => {
		if (current) lastSelected = current;
	});

	function select(p: ProcessInfo) {
		selectedPid = p.pid;
		lastSelected = p;
	}

	const time = $derived(
		view.snapshot?.takenAt ? timestampDate(view.snapshot.takenAt).toLocaleTimeString(locale) : ''
	);

	const statusText = $derived.by(() => {
		if (view.paused)
			return view.waiting
				? m.procs_status_paused_waiting({ time })
				: m.procs_status_paused({ time });
		switch (view.status) {
			case 'connecting':
				return m.procs_status_connecting();
			case 'live':
				return m.procs_status_live();
			case 'offline':
				return m.procs_status_offline();
			case 'reconnecting':
				return m.procs_status_reconnecting();
			case 'stopped':
				return m.procs_status_stopped();
			default:
				return '';
		}
	});

	const failureText = $derived.by(() => {
		switch (view.failure) {
			case 'forbidden':
				return m.procs_failed_forbidden();
			case 'not_found':
				return m.procs_failed_not_found();
			case 'too_many':
				return m.procs_failed_too_many();
			default:
				return '';
		}
	});

	const regexError = $derived(
		regex.error === 'too_long'
			? m.procs_regex_too_long({ max: String(MAX_REGEX) })
			: regex.error === 'invalid'
				? m.procs_regex_invalid({ detail: regex.detail ?? '' })
				: ''
	);
	// Once loaded, the drawer stays: it animates closing.
	let drawerLoaded = $state(false);
	$effect(() => {
		if (shownProcess) drawerLoaded = true;
	});
</script>

<div class="flex flex-col gap-4">
	<div class="flex flex-wrap items-center gap-2">
		<p
			aria-live="polite"
			class="text-sm {view.status === 'offline'
				? 'text-amber-700 dark:text-amber-400'
				: 'text-muted-foreground'}"
		>
			{statusText}
		</p>
		{#if view.status !== 'refused' && view.status !== 'failed' && view.snapshot}
			<Button
				variant="outline"
				size="sm"
				class="ml-auto"
				aria-pressed={view.paused}
				onclick={() => (view.paused ? view.resume() : view.pause())}
			>
				{#if view.paused}<Play aria-hidden="true" />{m.procs_resume()}{:else}<Pause
						aria-hidden="true"
					/>{m.procs_pause()}{/if}
			</Button>
		{/if}
	</div>

	{#if view.status === 'refused'}
		<div
			role="alert"
			class="flex flex-col gap-1 rounded-md border border-destructive/50 p-3 text-sm"
		>
			<p class="text-destructive">{m.procs_refused({ reason: view.reason })}</p>
			<p class="text-muted-foreground">{m.procs_refused_hint()}</p>
			<Button variant="outline" size="sm" class="self-start" onclick={() => view.start(nodeId)}>
				{m.retry()}
			</Button>
		</div>
	{:else if view.status === 'failed'}
		<div class="flex flex-col items-start gap-2">
			<p role="alert" class="text-sm text-destructive">{failureText}</p>
			<Button variant="outline" size="sm" onclick={() => view.start(nodeId)}>{m.retry()}</Button>
		</div>
	{/if}

	{#if view.snapshot}
		<ProcessSummary snapshot={view.snapshot} />

		<div role="group" aria-label={m.procs_filters()} class="flex flex-wrap items-end gap-3">
			<div class="flex flex-col gap-1 text-sm">
				<label for="procs-user">{m.procs_filter_user()}</label>
				<select
					id="procs-user"
					bind:value={user}
					class="h-8 rounded-md border border-input bg-transparent px-2 text-sm"
				>
					<option value="">{m.procs_filter_user_all()}</option>
					{#each userNames as u (u)}<option value={u}>{displayText(u)}</option>{/each}
				</select>
			</div>
			<label class="flex flex-col gap-1 text-sm">
				{m.procs_filter_name()}
				<input
					type="search"
					bind:value={name}
					class="h-8 rounded-md border border-input bg-transparent px-2 text-sm"
				/>
			</label>
			<label class="flex flex-col gap-1 text-sm">
				{m.procs_filter_regex()}
				<input
					type="text"
					bind:value={regexSource}
					spellcheck="false"
					autocomplete="off"
					aria-invalid={regexError !== ''}
					aria-describedby={regexError ? 'procs-regex-error' : undefined}
					class="h-8 rounded-md border border-input bg-transparent px-2 font-mono text-sm aria-invalid:border-destructive"
				/>
			</label>
			<label class="flex h-8 items-center gap-2 text-sm">
				<input type="checkbox" bind:checked={tree} class="size-4 accent-primary" />
				{m.procs_tree()}
			</label>
			<span class="ml-auto text-sm text-muted-foreground">
				{m.procs_count({
					shown: formatNumber(rows.filter((r) => !r.context).length, locale),
					total: formatNumber(processes.length, locale)
				})}
			</span>
		</div>
		{#if regexError}
			<p id="procs-regex-error" role="alert" class="text-sm text-destructive">{regexError}</p>
		{/if}
		{#if view.snapshot.omitted > 0}
			<p class="text-sm text-muted-foreground">
				{m.procs_omitted({ count: formatNumber(view.snapshot.omitted, locale) })}
			</p>
		{/if}

		{#if rows.length === 0}
			<p class="rounded-md border border-dashed border-border p-4 text-sm">
				{m.procs_none_match()}
			</p>
		{:else}
			<ProcessTable
				{rows}
				{sort}
				selected={selectedPid}
				onSort={(key) => (sort = nextSort(sort, key))}
				onSelect={select}
			/>
		{/if}
	{:else if view.status === 'connecting' || view.status === 'live' || view.status === 'reconnecting'}
		<p role="status" class="text-sm text-muted-foreground">{m.procs_waiting()}</p>
	{/if}
</div>

<!-- The details load with the first opened process (M2-34: on demand). -->
{#if shownProcess || drawerLoaded}
	{#await import('./ProcessDrawer.svelte') then { default: ProcessDrawer }}
		<ProcessDrawer
			process={shownProcess}
			parent={shownProcess ? byPid.get(shownProcess.ppid) : undefined}
			ended={shownProcess !== undefined && current === undefined}
			onClose={() => {
				selectedPid = undefined;
				lastSelected = undefined;
			}}
		/>
	{/await}
{/if}
