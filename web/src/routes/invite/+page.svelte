<script lang="ts">
	import AuthLayout from '#lib/components/auth/AuthLayout.svelte';
	import InvitationForm from '#lib/components/auth/InvitationForm.svelte';
	import { redirectTo } from '#lib/navigation/navigate.js';
	import { loginPath } from '#lib/navigation/redirect.js';
	import { m } from '#lib/paraglide/messages.js';

	// The token travels in the fragment, so it never reaches the server's access logs (ADR 0008).
	const token = location.hash.slice(1);
</script>

<svelte:head>
	<title>{m.invite_title()} · SerMon</title>
</svelte:head>

<AuthLayout title={m.invite_title()} description={m.invite_subtitle()}>
	{#if token}
		<InvitationForm
			{token}
			onAccepted={() => redirectTo(loginPath('invited'))}
			onJoined={() => redirectTo('/fleet')}
		/>
	{:else}
		<p role="alert" class="text-sm text-destructive">{m.invite_invalid()}</p>
	{/if}
</AuthLayout>
