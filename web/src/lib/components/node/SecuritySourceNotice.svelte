<script lang="ts">
	import ShieldAlert from '@lucide/svelte/icons/shield-alert';
	import ShieldQuestion from '@lucide/svelte/icons/shield-question';
	import { type Node, SecuritySource } from '#lib/api/gen/sermon/web/v1/node_pb.js';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import { m } from '#lib/paraglide/messages.js';

	/**
	 * Where the node's agent reads security events (M2-31), shown only when it matters: no source
	 * at all (no journald, no readable log file), or a log file whose events are not verified.
	 * The journal, the rsyslog template and older agents that report nothing show nothing.
	 */
	let { node }: { node: Node } = $props();
</script>

{#if node.securitySource === SecuritySource.NONE}
	<div
		role="status"
		class="flex items-start gap-2 rounded-md border border-amber-500/50 bg-amber-500/10 p-3 text-sm"
	>
		<ShieldAlert
			class="mt-0.5 size-4 shrink-0 text-amber-700 dark:text-amber-400"
			aria-hidden="true"
		/>
		<div class="flex flex-col gap-1">
			<p class="flex items-center gap-1 font-medium">
				{m.security_source_none_title()}
				<HelpTip topic="concepts/security-source" />
			</p>
			<p class="text-muted-foreground">
				{m.security_source_none_text()}
				<code class="font-mono">install.sh --grant-log-access</code>
				{m.security_source_none_or()}
			</p>
		</div>
	</div>
{:else if node.securitySource === SecuritySource.FILE_UNVERIFIED}
	<p role="status" class="flex items-center gap-1 text-sm text-muted-foreground">
		<ShieldQuestion class="size-4" aria-hidden="true" />
		{m.security_source_unverified()}
		<HelpTip topic="concepts/unverified-source" />
	</p>
{/if}
