<script lang="ts">
	import Bug from '@lucide/svelte/icons/bug';
	import Info from '@lucide/svelte/icons/info';
	import OctagonAlert from '@lucide/svelte/icons/octagon-exclamation-point';
	import TriangleAlert from '@lucide/svelte/icons/triangle-exclamation-point';
	import { LogSeverity } from '#lib/api/gen/sermon/web/v1/log_settings_pb.js';
	import { groupOf } from '#lib/logs/histogram.js';
	import { severityLabel } from '#lib/logs/labels.js';

	/** The severity of a line with icon and text – never by colour alone (KONZEPT §17). */
	let { severity }: { severity: LogSeverity } = $props();

	const look = $derived.by(() => {
		switch (groupOf(severity)) {
			case 'error':
				return { icon: OctagonAlert, tone: 'text-red-700 dark:text-red-400' };
			case 'warning':
				return { icon: TriangleAlert, tone: 'text-amber-700 dark:text-amber-400' };
			case 'info':
				return { icon: Info, tone: 'text-sky-700 dark:text-sky-400' };
			default:
				return { icon: Bug, tone: 'text-muted-foreground' };
		}
	});
</script>

<span class="inline-flex items-center gap-1 text-xs font-medium {look.tone}">
	<look.icon class="size-3.5 shrink-0" aria-hidden="true" />
	{severityLabel(severity)}
</span>
