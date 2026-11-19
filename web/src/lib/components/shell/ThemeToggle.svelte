<script lang="ts">
	import Monitor from '@lucide/svelte/icons/monitor';
	import Moon from '@lucide/svelte/icons/moon';
	import Sun from '@lucide/svelte/icons/sun';
	import { Button } from '#lib/components/ui/button/index.js';
	import { m } from '#lib/paraglide/messages.js';
	import { themeState, type Theme } from '#lib/theme/theme.svelte.js';

	const options: { theme: Theme; icon: typeof Sun; label: () => string }[] = [
		{ theme: 'light', icon: Sun, label: () => m.theme_light() },
		{ theme: 'dark', icon: Moon, label: () => m.theme_dark() },
		{ theme: 'system', icon: Monitor, label: () => m.theme_system() }
	];
</script>

<div role="group" aria-label={m.theme_label()} class="inline-flex gap-1">
	{#each options as option (option.theme)}
		<Button
			variant={themeState.current === option.theme ? 'secondary' : 'ghost'}
			size="icon-sm"
			aria-label={option.label()}
			title={option.label()}
			aria-pressed={themeState.current === option.theme}
			onclick={() => themeState.set(option.theme)}
		>
			<option.icon aria-hidden="true" />
		</Button>
	{/each}
</div>
