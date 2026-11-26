<script lang="ts">
	import ShieldAlert from '@lucide/svelte/icons/shield-alert';
	import type { UpdatePackage } from '#lib/api/gen/sermon/web/v1/update_pb.js';
	import { displayText } from '#lib/logs/text.js';
	import { m } from '#lib/paraglide/messages.js';

	/**
	 * The available updates of a node (M4-14): security updates marked by an icon and a word, never
	 * by colour alone. Names and versions come from third-party package sources: shown as text,
	 * control characters made visible.
	 */
	let { packages, caption }: { packages: UpdatePackage[]; caption: string } = $props();
</script>

<div class="max-h-80 overflow-auto rounded-md border">
	<table class="w-full text-sm">
		<caption class="sr-only">{caption}</caption>
		<thead class="sticky top-0 bg-muted text-left">
			<tr>
				<th scope="col" class="px-2 py-1">{m.updates_package()}</th>
				<th scope="col" class="px-2 py-1">{m.updates_installed()}</th>
				<th scope="col" class="px-2 py-1">{m.updates_candidate()}</th>
				<th scope="col" class="px-2 py-1">{m.updates_source()}</th>
			</tr>
		</thead>
		<tbody>
			<!-- Unambiguous: a name with a space must not meet another package's (review M4-30). -->
			{#each packages as p (JSON.stringify([p.name, p.candidateVersion]))}
				<tr class="border-t">
					<td class="px-2 py-1 font-mono break-all">
						{displayText(p.name)}
						{#if p.security}
							<span class="ml-1 inline-flex items-center gap-0.5 text-red-700 dark:text-red-400">
								<ShieldAlert class="size-3.5" aria-hidden="true" />
								{m.updates_security()}
							</span>
						{/if}
					</td>
					<td class="px-2 py-1 font-mono break-all">{displayText(p.installedVersion) || '–'}</td>
					<td class="px-2 py-1 font-mono break-all">{displayText(p.candidateVersion)}</td>
					<td class="px-2 py-1 break-all">{displayText(p.source) || '–'}</td>
				</tr>
			{/each}
		</tbody>
	</table>
</div>
