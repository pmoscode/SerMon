<script lang="ts">
	import { page } from '$app/state';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import SsoIdentities from '#lib/components/security/SsoIdentities.svelte';
	import TwoFactorSettings from '#lib/components/security/TwoFactorSettings.svelte';
	import { m } from '#lib/paraglide/messages.js';
	import { sessionState } from '#lib/stores/session.svelte.js';
</script>

<svelte:head>
	<title>{m.security_title()} · SerMon</title>
</svelte:head>

<div class="mb-6 flex items-center gap-2">
	<h1 class="text-2xl font-semibold tracking-tight">{m.security_title()}</h1>
	<HelpTip topic="pages/security" />
</div>

{#if sessionState.secondFactorSetupRequired}
	<p role="alert" class="mb-4 max-w-2xl rounded-md border border-amber-500/50 p-3 text-sm">
		{m.security_setup_required_notice()}
	</p>
{/if}

<div class="flex flex-col gap-6">
	<TwoFactorSettings />
	<SsoIdentities taken={page.url.searchParams.get('sso') === 'taken'} />
</div>
