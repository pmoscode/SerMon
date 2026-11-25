<script lang="ts">
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import { m } from '#lib/paraglide/messages.js';

	/** The fleet's views (KONZEPT §16): the overview and its dashboards, below the top bar's Fleet. */
	const views = [
		{ href: resolve('/(app)/fleet'), label: () => m.fleet_view_overview() },
		{ href: resolve('/(app)/capacity'), label: () => m.capacity_title() },
		{ href: resolve('/(app)/heatmap'), label: () => m.heatmap_title() },
		{ href: resolve('/(app)/anomalies'), label: () => m.anomalies_title() }
	];
</script>

<nav aria-label={m.fleet_views()} class="flex gap-1 border-b text-sm">
	{#each views as v (v.href)}
		{@const current = page.url.pathname.startsWith(v.href)}
		<a
			href={v.href}
			aria-current={current ? 'page' : undefined}
			class="-mb-px border-b-2 px-3 py-1.5 text-muted-foreground hover:text-foreground aria-[current=page]:border-primary aria-[current=page]:text-foreground {current
				? ''
				: 'border-transparent'}"
		>
			{v.label()}
		</a>
	{/each}
</nav>
