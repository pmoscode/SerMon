<script lang="ts">
	import CircleCheck from '@lucide/svelte/icons/circle-check';
	import CircleOff from '@lucide/svelte/icons/circle-off';
	import PowerOff from '@lucide/svelte/icons/power-off';
	import OctagonAlert from '@lucide/svelte/icons/octagon-exclamation-point';
	import TriangleAlert from '@lucide/svelte/icons/triangle-exclamation-point';
	import type { Status } from '#lib/fleet/status.js';
	import { m } from '#lib/paraglide/messages.js';
	import { cn } from '#lib/utils.js';

	/** Status with icon and text – never by color alone (KONZEPT §17). */
	let { status, class: className = '' }: { status: Status; class?: string } = $props();

	const look = $derived(
		{
			ok: {
				icon: CircleCheck,
				label: m.status_ok(),
				tone: 'text-emerald-700 dark:text-emerald-400'
			},
			warning: {
				icon: TriangleAlert,
				label: m.status_warning(),
				tone: 'text-amber-700 dark:text-amber-400'
			},
			critical: {
				icon: OctagonAlert,
				label: m.status_critical(),
				tone: 'text-red-700 dark:text-red-400'
			},
			offline: { icon: CircleOff, label: m.status_offline(), tone: 'text-muted-foreground' },
			deactivated: { icon: PowerOff, label: m.status_deactivated(), tone: 'text-muted-foreground' }
		}[status]
	);
</script>

<span class={cn('inline-flex items-center gap-1 text-xs font-medium', look.tone, className)}>
	<look.icon class="size-3.5" aria-hidden="true" />
	{look.label}
</span>
