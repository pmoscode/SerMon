<script lang="ts">
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import LogExplorer from '#lib/components/logs/LogExplorer.svelte';
	import { filterParams, parseFilter, type LogFilter } from '#lib/logs/filter.js';
	import { m } from '#lib/paraglide/messages.js';

	/** The filter lives in the URL: views can be shared and charts and alerts link into it. */
	const filter = $derived(parseFilter(new URLSearchParams(page.url.search)));

	function apply(next: LogFilter) {
		const query = filterParams(next).toString();
		// eslint-disable-next-line svelte/no-navigation-without-resolve -- resolve() with the query of the filter
		void goto(`${resolve('/(app)/logs')}${query ? `?${query}` : ''}`, {
			reset: false
		});
	}
</script>

<svelte:head>
	<title>{m.logs_title()} · SerMon</title>
</svelte:head>

<div class="mb-6 flex flex-wrap items-center gap-2">
	<h1 class="text-2xl font-semibold tracking-tight">{m.logs_title()}</h1>
	<HelpTip topic="pages/logs" />
</div>

<LogExplorer {filter} onFilter={apply} />
