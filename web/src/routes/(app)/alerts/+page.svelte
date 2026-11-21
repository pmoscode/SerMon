<script lang="ts">
	import AlertsView from '#lib/components/alerts/AlertsView.svelte';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import { m } from '#lib/paraglide/messages.js';
</script>

<svelte:head>
	<title>{m.alerts_title()} · SerMon</title>
</svelte:head>

<div class="mb-6 flex flex-wrap items-center gap-2">
	<h1 class="text-2xl font-semibold tracking-tight">{m.alerts_title()}</h1>
	<HelpTip topic="pages/alerts" />
</div>

<AlertsView />
