<script lang="ts">
	import { onboardingApi } from '#lib/api/onboarding.js';
	import { redirectTo } from '#lib/navigation/navigate.js';
	import { HOME } from '#lib/navigation/redirect.js';
	import { m } from '#lib/paraglide/messages.js';
	import { sessionState } from '#lib/stores/session.svelte.js';

	/** First start: the setup assistant; otherwise the app or the login. */
	async function route(): Promise<string> {
		const status = await onboardingApi.setup.getSetupStatus({}).catch(() => undefined);
		if (status?.setupRequired) return '/setup';
		const session = await sessionState.load().catch(() => 'anonymous');
		return session === 'authenticated' ? HOME : '/login';
	}

	$effect(() => {
		void route().then(redirectTo);
	});
</script>

<p class="p-8 text-muted-foreground" role="status">{m.loading()}</p>
