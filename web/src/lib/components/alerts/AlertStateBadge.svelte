<script lang="ts">
	import CircleCheck from '@lucide/svelte/icons/circle-check';
	import Hourglass from '@lucide/svelte/icons/hourglass';
	import OctagonAlert from '@lucide/svelte/icons/octagon-exclamation-point';
	import TriangleAlert from '@lucide/svelte/icons/triangle-exclamation-point';
	import { AlertState } from '#lib/api/gen/sermon/web/v1/alert_pb.js';
	import { m } from '#lib/paraglide/messages.js';
	import { cn } from '#lib/utils.js';

	/** State with icon and text – never by color alone (KONZEPT §17). */
	let { state, class: className = '' }: { state: AlertState; class?: string } = $props();

	const look = $derived.by(() => {
		switch (state) {
			case AlertState.CRITICAL:
				return {
					icon: OctagonAlert,
					label: m.alert_state_critical(),
					tone: 'text-red-700 dark:text-red-400'
				};
			case AlertState.WARNING:
				return {
					icon: TriangleAlert,
					label: m.alert_state_warning(),
					tone: 'text-amber-700 dark:text-amber-400'
				};
			case AlertState.PENDING:
				return { icon: Hourglass, label: m.alert_state_pending(), tone: 'text-muted-foreground' };
			case AlertState.RESOLVED:
				return {
					icon: CircleCheck,
					label: m.alert_state_resolved(),
					tone: 'text-emerald-700 dark:text-emerald-400'
				};
			default:
				return { icon: CircleCheck, label: m.alert_state_ok(), tone: 'text-muted-foreground' };
		}
	});
</script>

<span class={cn('inline-flex items-center gap-1 text-xs font-medium', look.tone, className)}>
	<look.icon class="size-3.5" aria-hidden="true" />
	{look.label}
</span>
