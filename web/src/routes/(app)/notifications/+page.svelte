<script lang="ts">
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import ChannelsPanel from '#lib/components/notify/ChannelsPanel.svelte';
	import DeliveriesPanel from '#lib/components/notify/DeliveriesPanel.svelte';
	import PoliciesPanel from '#lib/components/notify/PoliciesPanel.svelte';
	import WindowsPanel from '#lib/components/notify/WindowsPanel.svelte';
	import * as Tabs from '#lib/components/ui/tabs/index.js';
	import { m } from '#lib/paraglide/messages.js';
	import { sessionState } from '#lib/stores/session.svelte.js';
</script>

<svelte:head>
	<title>{m.notifications_title()} · SerMon</title>
</svelte:head>

<div class="mb-6 flex items-center gap-2">
	<h1 class="text-2xl font-semibold tracking-tight">{m.notifications_title()}</h1>
	<HelpTip topic="pages/notifications" />
</div>

{#if !sessionState.current?.orgAdmin}
	<p role="alert" class="text-sm">{m.admin_error_forbidden()}</p>
{:else}
	<Tabs.Root value="channels">
		<Tabs.List aria-label={m.notifications_title()}>
			<Tabs.Trigger value="channels">{m.channels_title()}</Tabs.Trigger>
			<Tabs.Trigger value="policies">{m.policies_title()}</Tabs.Trigger>
			<Tabs.Trigger value="windows">{m.windows_title()}</Tabs.Trigger>
			<Tabs.Trigger value="deliveries">{m.deliveries_title()}</Tabs.Trigger>
		</Tabs.List>
		<Tabs.Content value="channels" class="pt-4"><ChannelsPanel /></Tabs.Content>
		<Tabs.Content value="policies" class="pt-4"><PoliciesPanel /></Tabs.Content>
		<Tabs.Content value="windows" class="pt-4"><WindowsPanel /></Tabs.Content>
		<Tabs.Content value="deliveries" class="pt-4"><DeliveriesPanel /></Tabs.Content>
	</Tabs.Root>
{/if}
