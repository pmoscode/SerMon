<script lang="ts">
	import { formatPercent } from '#lib/format/format.js';
	import { colorOf, moveCursor } from '#lib/heatmap/heatmap.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { displayText } from '#lib/logs/text.js';
	import { m } from '#lib/paraglide/messages.js';

	/**
	 * Nodes × time as coloured cells (M4-27). The host names are text, the cells a canvas; the
	 * arrow keys and the pointer move a cursor whose node, time and value the line above names.
	 */
	let {
		rows,
		start,
		step,
		columns,
		label
	}: {
		rows: { nodeId: string; hostname: string; values: number[] }[];
		/** Start of the first column and the width of a column, in ms. */
		start: number;
		step: number;
		columns: number;
		/** What the cells show, e.g. "CPU". */
		label: string;
	} = $props();

	const ROW = 16;
	const id = $props.id();
	let canvas = $state<HTMLCanvasElement | undefined>();
	let width = $state(0);
	let cursor = $state<{ row: number; column: number } | undefined>();

	const locale = $derived(localeState.current);
	const time = $derived(
		new Intl.DateTimeFormat(locale, {
			dateStyle: step >= 3600_000 || columns * step > 86_400_000 ? 'short' : undefined,
			timeStyle: 'short'
		})
	);
	const shown = $derived(
		cursor && rows[cursor.row]
			? {
					host: displayText(rows[cursor.row].hostname),
					from: time.format(start + cursor.column * step),
					to: time.format(start + (cursor.column + 1) * step),
					value: rows[cursor.row].values[cursor.column]
				}
			: undefined
	);

	function token(name: string): string {
		return getComputedStyle(document.documentElement).getPropertyValue(name).trim() || '#888';
	}

	$effect(() => {
		if (!canvas || width === 0) return;
		const dpr = devicePixelRatio || 1;
		const height = rows.length * ROW;
		canvas.width = Math.round(width * dpr);
		canvas.height = Math.round(height * dpr);
		canvas.style.width = `${width}px`;
		canvas.style.height = `${height}px`;
		const ctx = canvas.getContext('2d');
		if (!ctx) return;
		ctx.scale(dpr, dpr);
		const cell = width / columns;
		rows.forEach((r, y) => {
			r.values.forEach((v, x) => {
				const color = colorOf(v);
				if (!color) return;
				ctx.fillStyle = color;
				// A pixel more against hairlines between the cells.
				ctx.fillRect(x * cell, y * ROW, cell + 0.5, ROW - 1);
			});
		});
		if (cursor) {
			ctx.strokeStyle = token('--foreground');
			ctx.lineWidth = 2;
			ctx.strokeRect(
				cursor.column * cell + 1,
				cursor.row * ROW + 1,
				Math.max(cell - 2, 2),
				ROW - 3
			);
		}
	});

	function onKey(e: KeyboardEvent) {
		const next = moveCursor(e.key, cursor ?? { row: 0, column: columns - 1 }, rows.length, columns);
		if (!next) return;
		e.preventDefault();
		cursor = next;
	}

	function onPointer(e: PointerEvent) {
		const box = (e.currentTarget as HTMLElement).getBoundingClientRect();
		const column = Math.floor(((e.clientX - box.left) / box.width) * columns);
		const row = Math.floor((e.clientY - box.top) / ROW);
		if (row >= 0 && row < rows.length && column >= 0 && column < columns) cursor = { row, column };
	}
</script>

<div class="flex flex-col gap-1">
	<p id="{id}-value" class="min-h-5 text-sm tabular-nums" role="status">
		{#if shown}
			{m.heatmap_cell({
				host: shown.host,
				from: shown.from,
				to: shown.to,
				value: shown.value < 0 ? m.heatmap_no_data() : formatPercent(shown.value, locale)
			})}
		{:else}
			<span class="text-muted-foreground">{m.heatmap_cursor_hint()}</span>
		{/if}
	</p>
	<div class="flex gap-2">
		<ul class="w-40 shrink-0 text-xs" aria-hidden="true">
			{#each rows as r (r.nodeId)}
				<li class="truncate leading-4" style:height="{ROW}px">{displayText(r.hostname)}</li>
			{/each}
		</ul>
		<div class="min-w-0 flex-1" bind:clientWidth={width}>
			<!-- A custom widget (role application): it takes the arrow keys itself. -->
			<!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions -->
			<div
				tabindex="0"
				role="application"
				aria-label={m.heatmap_grid_label({ label, rows: rows.length })}
				aria-describedby="{id}-value"
				class="rounded-sm focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
				onkeydown={onKey}
				onpointermove={onPointer}
				onpointerdown={onPointer}
				onfocus={() => (cursor ??= { row: 0, column: columns - 1 })}
			>
				<canvas bind:this={canvas} aria-hidden="true" class="block"></canvas>
			</div>
			<div
				class="flex justify-between pt-1 text-xs text-muted-foreground tabular-nums"
				aria-hidden="true"
			>
				<span>{time.format(start)}</span>
				<span>{time.format(start + (columns * step) / 2)}</span>
				<span>{time.format(start + columns * step)}</span>
			</div>
		</div>
	</div>
</div>
