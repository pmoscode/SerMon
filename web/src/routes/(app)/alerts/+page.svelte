<script lang="ts">
	import type { Alert } from '#lib/api/gen/sermon/web/v1/alert_pb.js';
	import AlertsView from '#lib/components/alerts/AlertsView.svelte';
	import SilencesPanel, { type SilenceDraft } from '#lib/components/alerts/SilencesPanel.svelte';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import * as Tabs from '#lib/components/ui/tabs/index.js';
	import { m } from '#lib/paraglide/messages.js';

	let tab = $state('alerts');
	let draft = $state<SilenceDraft | undefined>(undefined);

	/** "Mute" on an alert opens a silence for its rule on its node. */
	function silence(a: Alert) {
		draft = { groupIds: [a.groupId], nodeIds: [a.key?.nodeId ?? ''], rules: [a.key?.ruleId ?? ''] };
		tab = 'silences';
	}
</script>

<svelte:head>
	<title>{m.alerts_title()} · SerMon</title>
</svelte:head>

<div class="mb-6 flex flex-wrap items-center gap-2">
	<h1 class="text-2xl font-semibold tracking-tight">{m.alerts_title()}</h1>
	<HelpTip topic="pages/alerts" />
</div>

<Tabs.Root bind:value={tab}>
	<Tabs.List aria-label={m.alerts_title()}>
		<Tabs.Trigger value="alerts">{m.alerts_tab_active()}</Tabs.Trigger>
		<Tabs.Trigger value="silences">{m.alerts_tab_silences()}</Tabs.Trigger>
	</Tabs.List>
	<Tabs.Content value="alerts" class="pt-4"><AlertsView onSilence={silence} /></Tabs.Content>
	<Tabs.Content value="silences" class="pt-4"><SilencesPanel {draft} /></Tabs.Content>
</Tabs.Root>
