<script lang="ts">
	import { api } from '#lib/api/client.js';
	import { adminError } from '#lib/admin/admin.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { m } from '#lib/paraglide/messages.js';
	import { withStepUp } from '#lib/stores/stepup.svelte.js';

	/** Whether every member needs two-factor authentication (M1-03). */
	let required = $state<boolean | undefined>();
	let failure = $state('');
	let busy = $state(false);

	async function load() {
		try {
			required = (await api.securityPolicy.getSecurityPolicy({})).secondFactorRequired;
		} catch (error) {
			failure = adminError(error);
		}
	}

	$effect(() => {
		void load();
	});

	async function toggle() {
		busy = true;
		failure = '';
		try {
			await withStepUp(() => api.securityPolicy.setSecondFactorRequired({ required: !required }));
			await load();
		} catch (error) {
			failure = adminError(error, m.admin_2fa_own_first());
		} finally {
			busy = false;
		}
	}
</script>

<div class="flex max-w-2xl flex-col gap-3">
	{#if failure}
		<p role="alert" class="rounded-md border border-destructive/50 p-3 text-sm text-destructive">
			{failure}
		</p>
	{/if}
	<h2 class="text-lg font-medium">{m.admin_2fa_title()}</h2>
	<p class="text-sm text-muted-foreground">{m.admin_2fa_text()}</p>
	{#if required !== undefined}
		<p class="text-sm" role="status">{required ? m.admin_2fa_on() : m.admin_2fa_off()}</p>
		<Button
			class="self-start"
			variant={required ? 'outline' : 'default'}
			disabled={busy}
			onclick={toggle}
		>
			{required ? m.admin_2fa_disable() : m.admin_2fa_enable()}
		</Button>
	{/if}
</div>
