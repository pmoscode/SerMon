<script lang="ts">
	import type { Snippet } from 'svelte';
	import LanguageSwitcher from '#lib/components/LanguageSwitcher.svelte';
	import ThemeToggle from '#lib/components/shell/ThemeToggle.svelte';
	import * as Card from '#lib/components/ui/card/index.js';

	let { title, description, children }: { title: string; description: string; children: Snippet } =
		$props();
</script>

<div class="flex min-h-screen flex-col">
	<header class="flex justify-end gap-2 p-4">
		<ThemeToggle />
		<LanguageSwitcher />
	</header>
	<main id="content" class="flex flex-1 items-start justify-center px-4 pt-8 pb-16 sm:pt-16">
		<Card.Root class="w-full max-w-md">
			<Card.Header>
				<p class="text-sm font-semibold tracking-tight text-muted-foreground">SerMon</p>
				<h1 class="text-2xl font-semibold tracking-tight">{title}</h1>
				<Card.Description>{description}</Card.Description>
			</Card.Header>
			<Card.Content>
				{@render children()}
			</Card.Content>
		</Card.Root>
	</main>
</div>
