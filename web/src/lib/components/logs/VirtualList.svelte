<script lang="ts" generics="T">
	import type { Snippet } from 'svelte';
	import { SvelteMap } from 'svelte/reactivity';

	/**
	 * A list that renders only the rows near the visible part (thousands of log lines). Rows may
	 * differ in height: each one is measured once rendered, unmeasured ones count with the estimate.
	 */
	let {
		items,
		key,
		row,
		label,
		estimate = 48,
		overscan = 800,
		height: viewHeight = '60vh',
		class: className = ''
	}: {
		items: T[];
		key: (item: T) => string;
		row: Snippet<[T, number]>;
		/** The accessible name of the scrolling region. */
		label: string;
		estimate?: number;
		/** Pixels rendered beyond the visible part on both sides. */
		overscan?: number;
		/** The height of the scrolling region (CSS), so that it never grows with its rows. */
		height?: string;
		class?: string;
	} = $props();

	let viewport = $state<HTMLDivElement>();
	let scrollTop = $state(0);
	let height = $state(0);
	const heights = new SvelteMap<string, number>();

	const offsets = $derived.by(() => {
		const out = new Float64Array(items.length + 1);
		for (let i = 0; i < items.length; i++)
			out[i + 1] = out[i] + (heights.get(key(items[i])) ?? estimate);
		return out;
	});

	/** The index of the row at y. */
	function indexAt(y: number): number {
		let low = 0;
		let high = items.length;
		while (low < high) {
			const mid = (low + high + 1) >> 1;
			if (offsets[mid] <= y) low = mid;
			else high = mid - 1;
		}
		return Math.min(low, Math.max(items.length - 1, 0));
	}

	const start = $derived(indexAt(scrollTop - overscan));
	const end = $derived(Math.min(items.length, indexAt(scrollTop + (height || 800) + overscan) + 1));
	const total = $derived(offsets[items.length]);

	function measure(node: HTMLElement, id: string) {
		let current = id;
		const observer = new ResizeObserver(() => {
			const h = node.offsetHeight;
			if (h > 0 && heights.get(current) !== h) heights.set(current, h);
		});
		observer.observe(node);
		return {
			update(next: string) {
				current = next;
			},
			destroy() {
				observer.disconnect();
			}
		};
	}

	// Heights of rows that are gone are forgotten.
	$effect(() => {
		const present = new Set(items.map(key));
		for (const id of [...heights.keys()]) if (!present.has(id)) heights.delete(id);
	});

	/** Scrolls to the top (e.g. after a new query). */
	export function scrollToTop(): void {
		if (viewport) viewport.scrollTop = 0;
		scrollTop = 0;
	}

	/** Scrolls a row into the middle of the view. */
	export function scrollToIndex(index: number): void {
		if (!viewport) return;
		viewport.scrollTop = Math.max(0, offsets[index] - (viewport.clientHeight - estimate) / 2);
		scrollTop = viewport.scrollTop;
	}
</script>

<!-- A scrolling region must be reachable by keyboard. -->
<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
<div
	bind:this={viewport}
	bind:clientHeight={height}
	onscroll={() => (scrollTop = viewport?.scrollTop ?? 0)}
	style:height={viewHeight}
	style:overflow-y="auto"
	class="focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none {className}"
	tabindex="0"
	role="region"
	aria-label={label}
>
	<ol
		style:padding-top="{offsets[start] ?? 0}px"
		style:padding-bottom="{Math.max(0, total - (offsets[end] ?? total))}px"
	>
		{#each items.slice(start, end) as item, i (key(item))}
			<li use:measure={key(item)} aria-setsize={items.length} aria-posinset={start + i + 1}>
				{@render row(item, start + i)}
			</li>
		{/each}
	</ol>
</div>
