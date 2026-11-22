<script lang="ts">
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { m } from '#lib/paraglide/messages.js';
	import { QUOTA_FIELDS, type QuotaForm, quotaLabel } from '#lib/organizations/quotas.js';

	/** The quotas of an organization as text fields; empty is the instance default (M2-32). */
	let { form = $bindable(), idPrefix }: { form: QuotaForm; idPrefix: string } = $props();
</script>

<fieldset class="flex flex-col gap-2">
	<legend class="text-sm font-medium">{m.orgs_quotas()}</legend>
	<p class="text-sm text-muted-foreground">{m.orgs_quotas_hint()}</p>
	<div class="grid gap-3 sm:grid-cols-2">
		{#each QUOTA_FIELDS as field (field.key)}
			<div class="flex flex-col gap-1">
				<Label for="{idPrefix}-{field.key}">{quotaLabel(field.key)}</Label>
				<Input
					id="{idPrefix}-{field.key}"
					inputmode="numeric"
					autocomplete="off"
					placeholder={m.orgs_quotas_none()}
					bind:value={form[field.key]}
				/>
			</div>
		{/each}
	</div>
</fieldset>
