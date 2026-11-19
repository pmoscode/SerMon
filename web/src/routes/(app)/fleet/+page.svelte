<script lang="ts">
	import Server from '@lucide/svelte/icons/server';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import { m } from '#lib/paraglide/messages.js';
</script>

<svelte:head>
	<title>{m.fleet_title()} · SerMon</title>
</svelte:head>

<div class="mb-6 flex items-center gap-2">
	<h1 class="text-2xl font-semibold tracking-tight">{m.fleet_title()}</h1>
	<HelpTip topic="pages/fleet" />
</div>

<section
	class="flex flex-col items-center gap-3 rounded-lg border border-dashed p-12 text-center"
	aria-labelledby="fleet-empty"
>
	<Server class="size-10 text-muted-foreground" aria-hidden="true" />
	<h2 id="fleet-empty" class="text-lg font-medium">{m.fleet_empty_title()}</h2>
	<p class="max-w-md text-sm text-muted-foreground">{m.fleet_empty_text()}</p>
</section>
