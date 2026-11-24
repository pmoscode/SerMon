<script lang="ts">
	import CircleAlert from '@lucide/svelte/icons/circle-alert';
	import CircleCheck from '@lucide/svelte/icons/circle-check';
	import Sparkles from '@lucide/svelte/icons/sparkles';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import { timestampMs } from '@bufbuild/protobuf/wkt';
	import { resolve } from '$app/paths';
	import type { SecurityEvent } from '#lib/api/gen/sermon/web/v1/security_event_pb.js';
	import HelpTip from '#lib/components/help/HelpTip.svelte';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { alertLogsQuery } from '#lib/logs/filter.js';
	import { displayText } from '#lib/logs/text.js';
	import { m } from '#lib/paraglide/messages.js';
	import {
		countryName,
		detailText,
		failureKind,
		kindLabel,
		remoteText
	} from '#lib/security/labels.js';

	/**
	 * The events of the filter (KONZEPT §16.4): time · node · result · user · source · details,
	 * marked new or unverified with icon and text. Texts of the hosts are shown as text only.
	 */
	let {
		events,
		names,
		now,
		showNode = true
	}: {
		events: SecurityEvent[];
		names: Map<string, string>;
		now: number;
		showNode?: boolean;
	} = $props();

	const locale = $derived(localeState.current);
	const time = (e: SecurityEvent) =>
		e.occurredAt
			? new Intl.DateTimeFormat(locale, { dateStyle: 'short', timeStyle: 'medium' }).format(
					timestampMs(e.occurredAt)
				)
			: '–';
	const logsQuery = (e: SecurityEvent) =>
		alertLogsQuery(e.nodeId, e.occurredAt ? timestampMs(e.occurredAt) : now, now);
</script>

<div class="overflow-x-auto rounded-md border border-border">
	<table class="w-full min-w-[56rem] text-sm">
		<caption class="sr-only">{m.secdash_table_title()}</caption>
		<thead class="bg-muted text-left text-xs">
			<tr>
				<th scope="col" class="px-2 py-1.5 font-medium">{m.secdash_col_time()}</th>
				{#if showNode}<th scope="col" class="px-2 py-1.5 font-medium">{m.secdash_col_node()}</th
					>{/if}
				<th scope="col" class="px-2 py-1.5 font-medium">{m.secdash_col_result()}</th>
				<th scope="col" class="px-2 py-1.5 font-medium">{m.secdash_col_user()}</th>
				<th scope="col" class="px-2 py-1.5 font-medium">{m.secdash_col_source()}</th>
				<th scope="col" class="px-2 py-1.5 font-medium">{m.secdash_col_details()}</th>
				<th scope="col" class="px-2 py-1.5 font-medium">
					<span class="inline-flex items-center gap-0.5">
						{m.secdash_col_marks()}<HelpTip topic="concepts/unverified-source" />
					</span>
				</th>
				<th scope="col" class="px-2 py-1.5 font-medium"
					><span class="sr-only">{m.secdash_col_logs()}</span></th
				>
			</tr>
		</thead>
		<tbody>
			{#each events as e (e.id)}
				<tr class="border-t border-border align-top">
					<td class="px-2 py-1.5 whitespace-nowrap tabular-nums">{time(e)}</td>
					{#if showNode}
						<td class="px-2 py-1.5 whitespace-nowrap"
							>{names.get(e.nodeId) ?? e.nodeId.slice(0, 8)}</td
						>
					{/if}
					<td class="px-2 py-1.5 whitespace-nowrap">
						<span
							class="inline-flex items-center gap-1 {failureKind(e.kind)
								? 'text-red-700 dark:text-red-400'
								: ''}"
						>
							{#if failureKind(e.kind)}<CircleAlert
									class="size-3.5"
									aria-hidden="true"
								/>{:else if e.kind === 'ssh.login.success'}<CircleCheck
									class="size-3.5"
									aria-hidden="true"
								/>{/if}
							{kindLabel(e.kind)}
						</span>
					</td>
					<td class="max-w-48 truncate px-2 py-1.5 font-mono" title={displayText(e.user)}>
						{#if e.user === '' && e.kind.startsWith('ssh.')}<span
								class="text-muted-foreground italic">{m.secdash_empty_user()}</span
							>{:else}{displayText(e.user)}{/if}
					</td>
					<td class="px-2 py-1.5 font-mono whitespace-nowrap"
						>{remoteText(e)}{#if e.country}
							<span
								class="ml-1 font-sans text-xs text-muted-foreground"
								title={countryName(e.country, locale)}
								>{e.country}<span class="sr-only">: {countryName(e.country, locale)}</span></span
							>{/if}</td
					>
					<td class="max-w-80 px-2 py-1.5 break-all text-muted-foreground"
						>{detailText(e, locale)}</td
					>
					<td class="px-2 py-1.5">
						<span class="flex flex-col gap-0.5 text-xs">
							{#if e.newSource}
								<span
									class="inline-flex items-center gap-0.5 font-medium text-sky-700 dark:text-sky-400"
								>
									<Sparkles class="size-3" aria-hidden="true" />{m.secdash_mark_new()}
								</span>
							{/if}
							{#if e.unverified}
								<span class="inline-flex items-center gap-0.5 text-amber-700 dark:text-amber-400">
									<TriangleAlert class="size-3" aria-hidden="true" />{m.secdash_mark_unverified()}
								</span>
							{/if}
						</span>
					</td>
					<td class="px-2 py-1.5 whitespace-nowrap">
						<!-- eslint-disable svelte/no-navigation-without-resolve -- resolve() with the query of the filter -->
						<a
							href={`${resolve('/(app)/logs')}${logsQuery(e)}`}
							class="text-xs text-primary underline-offset-2 hover:underline"
							aria-label={m.secdash_logs_of({ time: time(e) })}>{m.secdash_col_logs()}</a
						>
						<!-- eslint-enable svelte/no-navigation-without-resolve -->
					</td>
				</tr>
			{/each}
		</tbody>
	</table>
</div>
