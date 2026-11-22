<script lang="ts">
	import type { OrganizationSummary } from '#lib/api/gen/sermon/web/v1/organization_pb.js';
	import { adminApi } from '#lib/api/admin.js';
	import { adminError } from '#lib/admin/admin.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { m } from '#lib/paraglide/messages.js';
	import { QUOTA_FIELDS, quotaForm, quotaLabel, quotasFrom } from '#lib/organizations/quotas.js';
	import { withStepUp } from '#lib/stores/stepup.svelte.js';
	import QuotaFields from './QuotaFields.svelte';

	/**
	 * Name and quotas of an organization (M2-32), for the superadmin; saving needs a step-up. The
	 * server checks everything again.
	 */
	let {
		organization,
		onDone
	}: { organization: OrganizationSummary; onDone: (saved: boolean) => void } = $props();

	// The form starts from the organization once; later changes of the prop don't reset it.
	const initial = (() => organization)();
	let name = $state(initial.name);
	let form = $state(quotaForm(initial.quotas));
	let failure = $state('');
	let busy = $state(false);

	async function save(event: SubmitEvent) {
		event.preventDefault();
		failure = '';
		const parsed = quotasFrom(form);
		if (!parsed.ok) {
			const field = QUOTA_FIELDS.find((f) => f.key === parsed.field)!;
			failure = m.orgs_quota_invalid({ field: quotaLabel(field.key), max: field.max });
			return;
		}
		busy = true;
		try {
			await withStepUp(() =>
				adminApi.organization.updateOrganization({
					organizationId: organization.id,
					name: name.trim(),
					quotas: parsed.quotas
				})
			);
			onDone(true);
		} catch (error) {
			failure = adminError(error);
		} finally {
			busy = false;
		}
	}
</script>

<form
	class="flex flex-col gap-4 rounded-lg border p-4"
	aria-label={m.orgs_edit_label({ name: organization.name })}
	onsubmit={save}
>
	{#if failure}
		<p role="alert" class="rounded-md border border-destructive/50 p-3 text-sm text-destructive">
			{failure}
		</p>
	{/if}
	<div class="flex flex-col gap-1">
		<Label for="org-edit-name">{m.orgs_name()}</Label>
		<Input id="org-edit-name" class="max-w-sm" bind:value={name} maxlength={200} required />
	</div>
	<QuotaFields bind:form idPrefix="org-edit" />
	<div class="flex gap-2">
		<Button type="submit" disabled={busy || !name.trim()}>{m.admin_save()}</Button>
		<Button type="button" variant="ghost" onclick={() => onDone(false)}>{m.stepup_cancel()}</Button>
	</div>
</form>
