<script lang="ts">
	import DashboardGrid from '#lib/components/dashboards/DashboardGrid.svelte';
	import WidgetView from '#lib/components/dashboards/WidgetView.svelte';
	import { layoutOf } from '#lib/dashboards/model.js';
	import type { KioskState } from '#lib/kiosk/kiosk.svelte.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { displayText } from '#lib/logs/text.js';
	import { m } from '#lib/paraglide/messages.js';
	import { timeRange } from '#lib/stores/timerange.svelte.js';

	/**
	 * A kiosk display (M5-27): the current board in full, no controls; a stale hint when the data
	 * is not current, nothing at all for a token that stopped working.
	 */
	let { kiosk, configured }: { kiosk: KioskState; configured: boolean } = $props();

	const board = $derived(kiosk.current);
	const spec = $derived(board?.spec);
	// Stable values for the widgets: the board object is new after every status of a widget.
	const token = $derived(board?.token ?? '');
	const source = $derived(board?.source);
	const titles = $derived(Object.fromEntries((spec?.widgets ?? []).map((w) => [w.id, w.title])));
	const shown = $derived(kiosk.shown.length);
	const position = $derived(shown > 1 ? (kiosk.index % shown) + 1 : 0);
	const clock = $derived(new Intl.DateTimeFormat(localeState.current, { timeStyle: 'short' }));

	function contentHeight(rows: number): number {
		return rows * 64 + (rows - 1) * 12 - 46;
	}
</script>

<main id="content" class="flex min-h-screen flex-col gap-3 p-4">
	{#if !configured}
		<p role="alert" class="m-auto max-w-xl text-center text-lg">{m.kiosk_no_token()}</p>
	{:else if kiosk.allInvalid}
		<p role="alert" class="m-auto max-w-xl text-center text-lg">{m.kiosk_invalid()}</p>
	{:else if !board || !spec}
		{#if kiosk.boards.some((b) => b.status === 'offline')}
			<p role="alert" class="m-auto text-lg">{m.kiosk_offline()}</p>
		{:else}
			<p role="status" class="m-auto text-lg text-muted-foreground">{m.loading()}</p>
		{/if}
	{:else}
		<header class="flex flex-wrap items-baseline gap-x-4 gap-y-1">
			<h1 class="text-2xl font-semibold tracking-tight">{displayText(spec.title)}</h1>
			{#if position}
				<span class="text-sm text-muted-foreground"
					>{m.kiosk_position({ n: position, of: shown })}</span
				>
			{/if}
			{#if board.staleSince !== undefined}
				<p
					role="status"
					class="rounded-md border border-amber-500 px-2 text-sm text-amber-700 dark:text-amber-400"
				>
					{m.kiosk_stale({ time: clock.format(board.staleSince) })}
				</p>
			{/if}
		</header>
		{#key token}
			<DashboardGrid
				items={layoutOf(spec)}
				{titles}
				label={m.dashboard_grid_label({ title: displayText(spec.title) })}
			>
				{#snippet widget(widgetId)}
					{@const w = spec.widgets.find((x) => x.id === widgetId)}
					{#if w && source}
						<WidgetView
							widget={w}
							{source}
							range={timeRange.range}
							height={contentHeight(w.position?.h ?? 1)}
							onstatus={(ok) => kiosk.widgetStatus(token, w.id, ok)}
						/>
					{/if}
				{/snippet}
			</DashboardGrid>
		{/key}
	{/if}
</main>
