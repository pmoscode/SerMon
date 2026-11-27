<script lang="ts">
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import { resolve } from '$app/paths';
	import type { InstanceProblem } from '#lib/api/gen/sermon/web/v1/auth_pb.js';
	import { m } from '#lib/paraglide/messages.js';
	import { sessionState } from '#lib/stores/session.svelte.js';

	/**
	 * The server's own problems (M2-24): only for superadmins, below the header. The mail tells
	 * them too; here they see it after the login.
	 */
	const problems = $derived(
		sessionState.current?.superadmin ? (sessionState.current.instanceProblems ?? []) : []
	);
	const critical = $derived(problems.some((p) => p.level === 'critical'));

	function text(p: InstanceProblem): string {
		switch (p.check) {
			case 'ingest_lag':
				return m.instance_ingest_lag({ seconds: Math.round(p.value) });
			case 'dropped_samples':
				return m.instance_dropped_samples({ count: Math.round(p.value) });
			case 'dropped_security_events':
				return m.instance_dropped_security_events({ count: Math.round(p.value) });
			case 'database_size':
				return m.instance_database_size({ percent: Math.round(p.value * 100) });
			case 'logs_unavailable':
				return m.instance_logs_unavailable();
			case 'agent_ca_expiry':
				return p.value > 0
					? m.instance_agent_ca_expiry({ days: Math.floor(p.value) })
					: m.instance_agent_ca_expired();
			default:
				return p.check;
		}
	}
</script>

{#if problems.length}
	<div
		role="status"
		class="border-b px-4 py-2 text-sm {critical
			? 'bg-red-50 text-red-800 dark:bg-red-950 dark:text-red-200'
			: 'bg-amber-50 text-amber-900 dark:bg-amber-950 dark:text-amber-200'}"
	>
		<div class="mx-auto flex max-w-7xl flex-wrap items-center gap-x-4 gap-y-1">
			<span class="inline-flex items-center gap-1 font-medium">
				<TriangleAlert class="size-4" aria-hidden="true" />
				{critical ? m.instance_critical() : m.instance_warning()}
			</span>
			{#each problems as p (p.check)}
				<span>
					{text(p)}
					{#if p.check === 'agent_ca_expiry'}
						– <a class="underline" href={resolve('/(app)/agent-ca')}>{m.instance_agent_ca_open()}</a
						>
					{/if}
				</span>
			{/each}
		</div>
	</div>
{/if}
