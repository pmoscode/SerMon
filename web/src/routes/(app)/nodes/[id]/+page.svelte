<script lang="ts">
	import { page } from '$app/state';
	import { m } from '#lib/paraglide/messages.js';
</script>

<svelte:head>
	<title>{m.node_title()} · SerMon</title>
</svelte:head>

<h1 class="text-2xl font-semibold tracking-tight">{m.node_title()}</h1>
<p class="text-sm text-muted-foreground">{page.params.id}</p>
