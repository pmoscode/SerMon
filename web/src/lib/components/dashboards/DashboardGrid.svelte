<script lang="ts">
	import GripVertical from '@lucide/svelte/icons/grip-vertical';
	import MoveDiagonal2 from '@lucide/svelte/icons/move-diagonal-2';
	import Pencil from '@lucide/svelte/icons/pencil';
	import X from '@lucide/svelte/icons/x';
	import type { Snippet } from 'svelte';
	import { nudge, place, type Item, type Key } from '#lib/dashboards/layout.js';
	import { m } from '#lib/paraglide/messages.js';

	/**
	 * The grid of a custom dashboard (ADR 0040): 12 columns of rows of 4rem. In the editor every
	 * widget has a handle: drag it or use the arrow keys to move, Shift + arrow keys (or the
	 * corner) to change the size; widgets in the way move down. Positions are set through the
	 * CSSOM (style: directive), the CSP needs no new hashes.
	 */
	let {
		items,
		titles,
		editable = false,
		label,
		onchange,
		onedit,
		onremove,
		widget
	}: {
		items: Item[];
		titles: Record<string, string>;
		editable?: boolean;
		/** The accessible name of the grid. */
		label: string;
		onchange?: (layout: Item[]) => void;
		onedit?: (id: string) => void;
		onremove?: (id: string) => void;
		widget: Snippet<[string]>;
	} = $props();

	const hint = $props.id();
	let grid = $state<HTMLUListElement>();
	let announcement = $state('');
	/** The layout while a drag is under way. */
	let preview = $state.raw<Item[] | undefined>();
	const shown = $derived(preview ?? items);

	function describe(layout: Item[], id: string): string {
		const i = layout.find((x) => x.id === id);
		if (!i) return '';
		return m.dashboard_grid_position({
			title: titles[id] ?? id,
			column: i.x + 1,
			row: i.y + 1,
			w: i.w,
			h: i.h
		});
	}

	function commit(layout: Item[] | undefined, id: string) {
		if (!layout) {
			announcement = m.dashboard_grid_blocked({ title: titles[id] ?? id });
			return;
		}
		onchange?.(layout);
		announcement = describe(layout, id);
	}

	const keys: string[] = ['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown'];

	function onkeydown(event: KeyboardEvent, id: string) {
		if (event.key === 'Delete' && onremove) {
			event.preventDefault();
			onremove(id);
			return;
		}
		if (!keys.includes(event.key) || event.altKey || event.ctrlKey || event.metaKey) return;
		event.preventDefault();
		const next = nudge(items, id, event.key as Key, event.shiftKey);
		// At an edge nothing moves; say where the widget is.
		if (next === undefined && items.some((i) => i.id === id)) {
			announcement = describe(items, id);
			return;
		}
		commit(next, id);
	}

	/** Cell size in pixels including the gap. */
	function cells(): { w: number; h: number } {
		if (!grid) return { w: 1, h: 1 };
		const style = getComputedStyle(grid);
		const gap = parseFloat(style.columnGap) || 0;
		const rowGap = parseFloat(style.rowGap) || 0;
		const row = parseFloat(style.gridAutoRows) || 64;
		return { w: (grid.clientWidth + gap) / 12, h: row + rowGap };
	}

	function startDrag(event: PointerEvent, id: string, resize: boolean) {
		if (!editable || event.button !== 0) return;
		const start = items.find((i) => i.id === id);
		if (!start) return;
		event.preventDefault();
		const target = event.currentTarget as HTMLElement;
		try {
			target.setPointerCapture(event.pointerId);
		} catch {
			// Synthetic events have no active pointer; the drag works without capture.
		}
		const size = cells();
		const x0 = event.clientX;
		const y0 = event.clientY;
		const base = items;
		const move = (e: PointerEvent) => {
			const dx = Math.round((e.clientX - x0) / size.w);
			const dy = Math.round((e.clientY - y0) / size.h);
			const box = resize
				? { x: start.x, y: start.y, w: start.w + dx, h: start.h + dy }
				: { x: start.x + dx, y: start.y + dy, w: start.w, h: start.h };
			preview = place(base, id, box) ?? preview;
		};
		const end = (e: PointerEvent) => {
			target.removeEventListener('pointermove', move);
			target.removeEventListener('pointerup', end);
			target.removeEventListener('pointercancel', end);
			const result = e.type === 'pointerup' ? preview : undefined;
			preview = undefined;
			if (result && result !== base) commit(result, id);
		};
		target.addEventListener('pointermove', move);
		target.addEventListener('pointerup', end);
		target.addEventListener('pointercancel', end);
	}
</script>

<ul
	bind:this={grid}
	aria-label={label}
	class="grid auto-rows-[4rem] grid-cols-12 gap-3"
	data-editing={editable || undefined}
>
	{#each shown as item (item.id)}
		{@const title = titles[item.id] ?? item.id}
		<li
			aria-label={title}
			class="relative flex min-h-0 min-w-0 flex-col overflow-hidden rounded-lg border bg-card {editable
				? 'border-dashed'
				: ''} {preview?.some((p) => p.id === item.id) ? 'transition-none' : ''}"
			style:grid-column="{item.x + 1} / span {item.w}"
			style:grid-row="{item.y + 1} / span {item.h}"
		>
			<header class="flex min-w-0 items-center gap-1 border-b px-2 py-1">
				{#if editable}
					<button
						type="button"
						class="cursor-move touch-none rounded-sm p-0.5 text-muted-foreground hover:text-foreground focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
						aria-label={m.dashboard_grid_move({ title })}
						aria-describedby={hint}
						aria-keyshortcuts="ArrowLeft ArrowRight ArrowUp ArrowDown Shift+ArrowLeft Shift+ArrowRight Shift+ArrowUp Shift+ArrowDown Delete"
						onkeydown={(e) => onkeydown(e, item.id)}
						onpointerdown={(e) => startDrag(e, item.id, false)}
					>
						<GripVertical class="size-4" aria-hidden="true" />
					</button>
				{/if}
				<h2 class="min-w-0 flex-1 truncate text-sm font-medium">{title}</h2>
				{#if editable && onedit}
					<button
						type="button"
						class="rounded-sm p-0.5 text-muted-foreground hover:text-foreground focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
						aria-label={m.dashboard_widget_edit({ title })}
						onclick={() => onedit(item.id)}
					>
						<Pencil class="size-3.5" aria-hidden="true" />
					</button>
				{/if}
				{#if editable && onremove}
					<button
						type="button"
						class="rounded-sm p-0.5 text-muted-foreground hover:text-destructive focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
						aria-label={m.dashboard_widget_remove({ title })}
						onclick={() => onremove(item.id)}
					>
						<X class="size-3.5" aria-hidden="true" />
					</button>
				{/if}
			</header>
			<div class="min-h-0 flex-1 overflow-auto p-2">{@render widget(item.id)}</div>
			{#if editable}
				<!-- The pointer's way to resize; the keyboard uses Shift + arrow keys on the handle. -->
				<span
					aria-hidden="true"
					class="absolute right-0 bottom-0 cursor-se-resize touch-none p-0.5 text-muted-foreground"
					data-resize={item.id}
					onpointerdown={(e) => startDrag(e, item.id, true)}
				>
					<MoveDiagonal2 class="size-3.5" />
				</span>
			{/if}
		</li>
	{/each}
</ul>
{#if editable}
	<p id={hint} class="sr-only">{m.dashboard_grid_hint()}</p>
	<p role="status" class="sr-only">{announcement}</p>
{/if}
