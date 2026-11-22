<script lang="ts">
	import { timestampDate } from '@bufbuild/protobuf/wkt';
	import type { AuditEntry, Member } from '#lib/api/gen/sermon/web/v1/access_pb.js';
	import { adminApi } from '#lib/api/admin.js';
	import { adminError } from '#lib/admin/admin.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { formatTimestamp } from '#lib/format/format.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { m } from '#lib/paraglide/messages.js';

	/** The audit log of the organization, newest first, page by page. */
	let { members }: { members: Member[] } = $props();

	const PAGE = 50;
	let entries = $state<AuditEntry[]>([]);
	let more = $state(false);
	let failure = $state('');
	let busy = $state(false);
	const actor = (id: string) =>
		id ? (members.find((x) => x.userId === id)?.email ?? id) : m.admin_system();

	async function load(before = 0n) {
		busy = true;
		try {
			const res = await adminApi.access.listAuditLog({ beforeSeq: before, pageSize: PAGE });
			entries = before === 0n ? res.entries : [...entries, ...res.entries];
			more = res.entries.length === PAGE;
		} catch (error) {
			failure = adminError(error);
		} finally {
			busy = false;
		}
	}

	$effect(() => {
		void load();
	});
</script>

<div class="flex flex-col gap-3">
	{#if failure}
		<p role="alert" class="rounded-md border border-destructive/50 p-3 text-sm text-destructive">
			{failure}
		</p>
	{/if}
	<div class="overflow-x-auto rounded-lg border">
		<table class="w-full text-sm">
			<caption class="sr-only">{m.admin_tab_audit()}</caption>
			<thead class="bg-muted/50 text-left text-xs text-muted-foreground">
				<tr>
					<th scope="col" class="px-3 py-2 font-medium">{m.admin_audit_time()}</th>
					<th scope="col" class="px-3 py-2 font-medium">{m.admin_audit_actor()}</th>
					<th scope="col" class="px-3 py-2 font-medium">{m.admin_audit_action()}</th>
					<th scope="col" class="px-3 py-2 font-medium">{m.admin_audit_details()}</th>
				</tr>
			</thead>
			<tbody>
				{#each entries as entry (entry.seq)}
					<tr class="border-t align-top">
						<td class="px-3 py-2 whitespace-nowrap">
							{entry.time
								? formatTimestamp(timestampDate(entry.time).getTime() / 1000, localeState.current)
								: '–'}
						</td>
						<td class="px-3 py-2">{actor(entry.actorId)}</td>
						<td class="px-3 py-2 font-mono text-xs">{entry.action}</td>
						<td class="px-3 py-2 font-mono text-xs text-muted-foreground">
							{entry.target}
							{#each Object.entries(entry.details) as [key, value] (key)}
								<span class="block">{key}={value}</span>
							{/each}
						</td>
					</tr>
				{:else}
					<tr
						><td colspan="4" class="px-3 py-4 text-muted-foreground"
							>{busy ? m.loading() : m.admin_audit_none()}</td
						></tr
					>
				{/each}
			</tbody>
		</table>
	</div>
	{#if more}
		<Button
			variant="outline"
			class="self-start"
			disabled={busy}
			onclick={() => load(entries.at(-1)?.seq ?? 0n)}
		>
			{m.admin_audit_more()}
		</Button>
	{/if}
</div>
