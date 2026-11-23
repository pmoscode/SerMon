<script lang="ts">
	import ArrowDown from '@lucide/svelte/icons/arrow-down';
	import ArrowUp from '@lucide/svelte/icons/arrow-up';
	import type { ProcessInfo } from '#lib/api/gen/sermon/web/v1/live_view_pb.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { displayText } from '#lib/logs/text.js';
	import { m } from '#lib/paraglide/messages.js';
	import {
		cpuText,
		memoryText,
		processName,
		rssText,
		runtimeText,
		userText
	} from '#lib/processes/display.js';
	import type { ProcessRow, Sort, SortKey } from '#lib/processes/table.js';

	/**
	 * The processes as a table that renders only the rows near the visible part (up to 4,000 every
	 * 2 s). Rows have one height; the name opens the details. Nothing here acts on a process (D19).
	 */
	let {
		rows,
		sort,
		selected,
		onSort,
		onSelect
	}: {
		rows: ProcessRow[];
		sort: Sort;
		/** The PID shown in the details. */
		selected?: number;
		onSort: (key: SortKey) => void;
		onSelect: (process: ProcessInfo) => void;
	} = $props();

	const ROW = 32;
	const OVERSCAN = 12;

	let scrollTop = $state(0);
	let height = $state(0);
	const locale = $derived(localeState.current);
	const start = $derived(Math.max(0, Math.floor(scrollTop / ROW) - OVERSCAN));
	const end = $derived(
		Math.min(rows.length, Math.floor(scrollTop / ROW) + Math.ceil((height || 800) / ROW) + OVERSCAN)
	);

	const columns: { key: SortKey; label: () => string; width: string; end?: boolean }[] = [
		{ key: 'pid', label: m.procs_col_pid, width: '5rem', end: true },
		{ key: 'ppid', label: m.procs_col_ppid, width: '5rem', end: true },
		{ key: 'user', label: m.procs_col_user, width: '7rem' },
		{ key: 'state', label: m.procs_col_state, width: '4.5rem' },
		{ key: 'cpu', label: m.procs_col_cpu, width: '4.5rem', end: true },
		{ key: 'memory', label: m.procs_col_memory, width: '4.5rem', end: true },
		{ key: 'rss', label: m.procs_col_rss, width: '6rem', end: true },
		{ key: 'threads', label: m.procs_col_threads, width: '5rem', end: true },
		{ key: 'runtime', label: m.procs_col_runtime, width: '8rem', end: true },
		{ key: 'name', label: m.procs_col_name, width: '13rem' },
		{ key: 'command', label: m.procs_col_command, width: 'auto' }
	];

	const ariaSort = (key: SortKey) =>
		sort.key === key ? (sort.desc ? 'descending' : 'ascending') : undefined;
</script>

{#snippet na()}<span class="text-muted-foreground">{m.procs_na()}</span>{/snippet}

<!-- A scrolling region must be reachable by keyboard. -->
<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
<div
	bind:clientHeight={height}
	onscroll={(e) => (scrollTop = e.currentTarget.scrollTop)}
	style:height="60vh"
	style:overflow="auto"
	class="rounded-md border border-border focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
	tabindex="0"
	role="region"
	aria-label={m.procs_title()}
>
	<table
		style:table-layout="fixed"
		style:width="100%"
		style:min-width="72rem"
		class="text-sm"
		aria-rowcount={rows.length + 1}
	>
		<colgroup>
			{#each columns as c (c.key)}<col style:width={c.width} />{/each}
		</colgroup>
		<thead style:position="sticky" style:top="0" style:z-index="1" class="bg-muted">
			<tr aria-rowindex={1}>
				{#each columns as c (c.key)}
					<th scope="col" aria-sort={ariaSort(c.key)} class="p-0 font-medium">
						<button
							type="button"
							class="flex w-full items-center gap-1 px-2 py-1.5 hover:bg-accent focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none {c.end
								? 'justify-end'
								: ''}"
							onclick={() => onSort(c.key)}
						>
							{c.label()}
							{#if sort.key === c.key}
								{#if sort.desc}<ArrowDown class="size-3.5" aria-hidden="true" />{:else}<ArrowUp
										class="size-3.5"
										aria-hidden="true"
									/>{/if}
							{/if}
						</button>
					</th>
				{/each}
			</tr>
		</thead>
		<tbody class="font-mono text-xs tabular-nums">
			{#if start > 0}<tr aria-hidden="true" style:height="{start * ROW}px"></tr>{/if}
			{#each rows.slice(start, end) as row, i (row.process.pid)}
				{@const p = row.process}
				<tr
					aria-rowindex={start + i + 2}
					style:height="{ROW}px"
					class="border-t border-border {p.pid === selected ? 'bg-accent' : ''} {row.context
						? 'text-muted-foreground'
						: ''}"
				>
					<td class="truncate px-2 text-right">{p.pid}</td>
					<td class="truncate px-2 text-right">{p.ppid}</td>
					<td class="truncate px-2"
						>{#if userText(p) !== undefined}{userText(p)}{:else}{@render na()}{/if}</td
					>
					<td class="truncate px-2">{displayText(p.state) || m.procs_na()}</td>
					<td class="truncate px-2 text-right">{cpuText(p, locale)}</td>
					<td class="truncate px-2 text-right">{memoryText(p, locale) ?? m.procs_na()}</td>
					<td class="truncate px-2 text-right">{rssText(p, locale) ?? m.procs_na()}</td>
					<td class="truncate px-2 text-right">{p.threads}</td>
					<td class="truncate px-2 text-right">{runtimeText(p, locale) ?? m.procs_na()}</td>
					<td class="truncate px-2" style:padding-left="{0.5 + Math.min(row.depth, 16) * 0.75}rem">
						{#if row.depth > 0}<span aria-hidden="true" class="text-muted-foreground">└ </span>{/if}
						<button
							type="button"
							class="max-w-full truncate rounded-sm text-left underline-offset-2 hover:underline focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
							aria-label={m.procs_details({ name: processName(p), pid: String(p.pid) })}
							onclick={() => onSelect(p)}>{processName(p)}</button
						>
						{#if row.context}<span class="sr-only">({m.procs_context_row()})</span>{/if}
					</td>
					<td class="truncate px-2">
						{#if p.cmdlineHidden}
							<span class="text-muted-foreground italic">{m.procs_hidden()}</span>
						{:else if p.cmdline === undefined}
							{@render na()}
						{:else}
							{displayText(p.cmdline) || '–'}
						{/if}
					</td>
				</tr>
			{/each}
			{#if end < rows.length}
				<tr aria-hidden="true" style:height="{(rows.length - end) * ROW}px"></tr>
			{/if}
		</tbody>
	</table>
</div>
