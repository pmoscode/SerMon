<script lang="ts">
	import { m } from '#lib/paraglide/messages.js';
	import { sessionState } from '#lib/stores/session.svelte.js';

	const organizations = $derived(sessionState.current?.organizations ?? []);
	const active = $derived(sessionState.current?.activeOrganizationId ?? '');

	let busy = $state(false);
	let failed = $state(false);

	async function change(event: Event & { currentTarget: HTMLSelectElement }) {
		const select = event.currentTarget;
		const id = select.value;
		busy = true;
		failed = false;
		try {
			await sessionState.switchOrganization(id);
		} catch {
			failed = true;
			select.value = active;
		} finally {
			busy = false;
		}
	}
</script>

{#if organizations.length > 1}
	<div class="flex items-center gap-2">
		<label for="org-switcher" class="sr-only">{m.org_label()}</label>
		<select
			id="org-switcher"
			class="h-8 max-w-48 truncate rounded-md border border-input bg-background px-2 text-sm focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
			value={active}
			disabled={busy}
			onchange={change}
		>
			{#each organizations as org (org.id)}
				<option value={org.id}>{org.name}</option>
			{/each}
		</select>
		{#if failed}
			<span role="alert" class="text-sm text-destructive">{m.org_switch_failed()}</span>
		{/if}
	</div>
{:else}
	<span class="max-w-48 truncate text-sm text-muted-foreground">
		<span class="sr-only">{m.org_label()}:</span>
		{sessionState.organization?.name ?? m.org_none()}
	</span>
{/if}
