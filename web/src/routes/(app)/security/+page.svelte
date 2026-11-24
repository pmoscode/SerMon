<script lang="ts">
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import SecurityDashboard from '#lib/components/securityevents/SecurityDashboard.svelte';
	import { m } from '#lib/paraglide/messages.js';
	import {
		parseSecurityFilter,
		securityFilterParams,
		type SecurityFilter
	} from '#lib/security/filter.js';

	/** The filter lives in the URL: views can be shared and node pages link into it. */
	const filter = $derived(parseSecurityFilter(new URLSearchParams(page.url.search)));

	function apply(next: SecurityFilter) {
		const query = securityFilterParams(next).toString();
		// eslint-disable-next-line svelte/no-navigation-without-resolve -- resolve() with the query of the filter
		void goto(`${resolve('/(app)/security')}${query ? `?${query}` : ''}`, { reset: false });
	}
</script>

<svelte:head>
	<title>{m.secdash_title()} · SerMon</title>
</svelte:head>

<div class="mb-6 flex flex-wrap items-center gap-2">
	<h1 class="text-2xl font-semibold tracking-tight">{m.secdash_title()}</h1>
	<HelpTip topic="pages/security_events" />
</div>

<SecurityDashboard {filter} onFilter={apply} />
