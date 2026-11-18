<script lang="ts">
	import LanguageSwitcher from '#lib/components/LanguageSwitcher.svelte';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { m } from '#lib/paraglide/messages.js';
</script>

<main class="mx-auto flex min-h-screen max-w-2xl flex-col justify-center gap-6 p-8">
	{#key localeState.current}
		<h1 class="text-4xl font-semibold tracking-tight">{m.welcome_title()}</h1>
		<p class="text-lg text-muted-foreground">{m.welcome_subtitle()}</p>
		<p class="font-mono text-sm text-muted-foreground">{m.status_under_construction()}</p>
	{/key}
	<LanguageSwitcher />
</main>
