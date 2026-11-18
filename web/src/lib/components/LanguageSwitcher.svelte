<script lang="ts">
	import { Button } from '#lib/components/ui/button/index.js';
	import { localeNames, localeState, locales, switchLocale } from '#lib/i18n/locale.svelte.js';
	import { m } from '#lib/paraglide/messages.js';
</script>

<div role="group" aria-label={m.language_label()} class="inline-flex gap-1">
	{#each locales as locale (locale)}
		<Button
			variant={localeState.current === locale ? 'secondary' : 'ghost'}
			size="sm"
			lang={locale}
			aria-pressed={localeState.current === locale}
			onclick={() => switchLocale(locale)}
		>
			{localeNames[locale]}
		</Button>
	{/each}
</div>
