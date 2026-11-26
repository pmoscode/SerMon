<script lang="ts">
	import Lightbulb from '@lucide/svelte/icons/lightbulb';
	import { resolve } from '$app/paths';
	import { modulesApi } from '#lib/api/modules.js';
	import type { NodeModuleHint } from '#lib/api/gen/sermon/web/v1/module_pb.js';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import { displayText } from '#lib/logs/text.js';
	import { moduleLabel } from '#lib/modules/labels.js';
	import { enableQuery } from '#lib/modules/link.js';
	import { m } from '#lib/paraglide/messages.js';
	import { sessionState } from '#lib/stores/session.svelte.js';

	/**
	 * The fleet's open suggestions (ADR 0039, M5-07): "nginx detected on web-01 – enable the
	 * module?" for modules a node's agent found and its group doesn't run. Nothing while there are
	 * none or they can't be loaded (the fleet stays as it is).
	 */
	const SHOWN = 5;
	let hints = $state.raw<NodeModuleHint[]>([]);

	$effect(() => {
		modulesApi.modules
			.listModuleHints({})
			.then((res) => (hints = res.hints))
			.catch(() => (hints = []));
	});
</script>

{#if hints.length > 0}
	<section
		class="flex flex-col gap-2 rounded-md border border-primary/40 bg-primary/5 p-3"
		aria-labelledby="module-hints-title"
	>
		<h2 id="module-hints-title" class="flex items-center gap-2 text-sm font-medium">
			<Lightbulb class="size-4" aria-hidden="true" />
			{m.modules_hints_title()}
			<HelpTip topic="concepts/modules" />
		</h2>
		<!-- eslint-disable svelte/no-navigation-without-resolve -- resolve() with the query of the form -->
		<ul class="flex flex-col gap-1 text-sm">
			{#each hints.slice(0, SHOWN) as h (h.nodeId + h.hint?.moduleId)}
				<li class="flex flex-wrap items-center gap-2" data-testid="fleet-module-hint">
					<span
						>{m.modules_hint_on({
							module: moduleLabel(h.hint?.moduleId ?? ''),
							host: displayText(h.hostname)
						})}</span
					>
					{#if sessionState.current?.orgAdmin}
						<a
							class="text-primary underline-offset-2 hover:underline"
							href={`${resolve('/(app)/admin')}?${enableQuery(h.groupId, h.hint?.moduleId ?? '', h.hint?.suggestedConfigJson)}`}
							>{m.modules_hint_enable()}</a
						>
					{:else}
						<a
							class="text-primary underline-offset-2 hover:underline"
							href={`${resolve('/(app)/nodes/[id]', { id: h.nodeId })}?tab=modules`}
							>{displayText(h.hostname)}</a
						>
					{/if}
				</li>
			{/each}
		</ul>
		<!-- eslint-enable svelte/no-navigation-without-resolve -->
		{#if hints.length > SHOWN}
			<p class="text-xs">
				{m.modules_hints_more({ count: hints.length - SHOWN })}
			</p>
		{/if}
	</section>
{/if}
