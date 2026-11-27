<script lang="ts">
	import AgentCARollover from '#lib/components/agentca/AgentCARollover.svelte';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import InstanceTabs from '#lib/components/shell/InstanceTabs.svelte';
	import { m } from '#lib/paraglide/messages.js';
	import { sessionState } from '#lib/stores/session.svelte.js';

	/** The agent CA of the instance, for superadmins (V1-19). */
</script>

<svelte:head>
	<title>{m.agentca_title()} · SerMon</title>
</svelte:head>

<InstanceTabs />

<div class="mb-6 flex items-center gap-2">
	<h1 class="text-2xl font-semibold tracking-tight">{m.agentca_title()}</h1>
	<HelpTip topic="pages/agent_ca" />
</div>

{#if !sessionState.current?.superadmin}
	<p role="alert" class="text-sm">{m.orgs_forbidden()}</p>
{:else}
	<AgentCARollover />
{/if}
