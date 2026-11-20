<script lang="ts">
	import AddNode from '#lib/components/admin/AddNode.svelte';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import { m } from '#lib/paraglide/messages.js';
</script>

<svelte:head>
	<title>{m.addnode_title()} · SerMon</title>
</svelte:head>

<div class="mb-6 flex items-center gap-2">
	<h1 class="text-2xl font-semibold tracking-tight">{m.addnode_title()}</h1>
	<HelpTip topic="pages/add_node" />
</div>

<AddNode />
