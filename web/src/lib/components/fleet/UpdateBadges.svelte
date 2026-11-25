<script lang="ts">
	import Power from '@lucide/svelte/icons/power';
	import ShieldAlert from '@lucide/svelte/icons/shield-alert';
	import type { NodeSummary } from '#lib/api/gen/sermon/web/v1/fleet_pb.js';
	import { m } from '#lib/paraglide/messages.js';

	/** Security updates and a needed reboot of a node (M4-15), each with an icon and a word. */
	let { node, class: className = '' }: { node: NodeSummary; class?: string } = $props();
</script>

{#if node.securityUpdates > 0 || node.rebootRequired}
	<span class="flex flex-wrap gap-1 text-xs {className}">
		{#if node.securityUpdates > 0}
			<span
				class="inline-flex items-center gap-0.5 rounded-sm border border-red-600/40 px-1 text-red-700 dark:text-red-400"
			>
				<ShieldAlert class="size-3" aria-hidden="true" />
				{m.fleet_badge_security({ count: node.securityUpdates })}
			</span>
		{/if}
		{#if node.rebootRequired}
			<span class="inline-flex items-center gap-0.5 rounded-sm border px-1">
				<Power class="size-3" aria-hidden="true" />
				{m.fleet_badge_reboot()}
			</span>
		{/if}
	</span>
{/if}
