<script lang="ts">
	import { timestampDate } from '@bufbuild/protobuf/wkt';
	import { alertingApi } from '#lib/api/alerting.js';
	import {
		DeliveryStatus,
		type Delivery,
		type NotificationChannel
	} from '#lib/api/gen/sermon/web/v1/notification_pb.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { m } from '#lib/paraglide/messages.js';

	/** The delivery log, newest first (kept 30 days); errors name the host, never secrets. */
	const PAGE = 50;
	let channels = $state<NotificationChannel[]>([]);
	let deliveries = $state<Delivery[]>([]);
	let channelId = $state('');
	let status = $state<'loading' | 'ready' | 'error'>('loading');
	let more = $state(false);

	async function load(append = false) {
		try {
			if (!append) channels = (await alertingApi.channels.listChannels({})).channels;
			const before = append ? (deliveries.at(-1)?.id ?? 0n) : 0n;
			const res = await alertingApi.channels.listDeliveries({
				channelId,
				beforeId: before,
				pageSize: PAGE
			});
			deliveries = append ? [...deliveries, ...res.deliveries] : res.deliveries;
			more = res.deliveries.length === PAGE;
			status = 'ready';
		} catch {
			status = 'error';
		}
	}

	$effect(() => {
		void channelId;
		void load();
	});

	const nameOf = (id: string) =>
		channels.find((c) => c.id === id)?.name ?? m.deliveries_deleted_channel();
	const timeFormat = $derived(
		new Intl.DateTimeFormat(localeState.current, { dateStyle: 'short', timeStyle: 'medium' })
	);

	function statusText(s: DeliveryStatus): string {
		switch (s) {
			case DeliveryStatus.SENT:
				return m.delivery_sent();
			case DeliveryStatus.FAILED:
				return m.delivery_failed();
			case DeliveryStatus.SENDING:
				return m.delivery_sending();
			default:
				return m.delivery_pending();
		}
	}
</script>

<div class="flex flex-col gap-4">
	<div class="flex flex-col gap-1">
		<Label for="deliveries-channel">{m.deliveries_channel()}</Label>
		<select
			id="deliveries-channel"
			class="h-9 w-64 rounded-md border border-input bg-background px-2 text-sm focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
			bind:value={channelId}
		>
			<option value="">{m.deliveries_all_channels()}</option>
			{#each channels as c (c.id)}<option value={c.id}>{c.name}</option>{/each}
		</select>
	</div>
	{#if status === 'loading'}
		<p role="status" class="text-muted-foreground">{m.loading()}</p>
	{:else if status === 'error'}
		<div class="flex items-center gap-3">
			<p role="alert" class="text-destructive">{m.error_unavailable()}</p>
			<Button variant="outline" onclick={() => load()}>{m.retry()}</Button>
		</div>
	{:else if deliveries.length === 0}
		<p class="text-sm text-muted-foreground">{m.deliveries_none()}</p>
	{:else}
		<div class="overflow-x-auto rounded-lg border">
			<table class="w-full text-sm">
				<caption class="sr-only">{m.deliveries_caption()}</caption>
				<thead class="bg-muted/50 text-left">
					<tr>
						<th scope="col" class="px-3 py-2 font-medium">{m.deliveries_time()}</th>
						<th scope="col" class="px-3 py-2 font-medium">{m.deliveries_channel()}</th>
						<th scope="col" class="px-3 py-2 font-medium">{m.deliveries_kind()}</th>
						<th scope="col" class="px-3 py-2 font-medium">{m.deliveries_rule()}</th>
						<th scope="col" class="px-3 py-2 font-medium">{m.deliveries_status()}</th>
						<th scope="col" class="px-3 py-2 font-medium">{m.deliveries_error()}</th>
					</tr>
				</thead>
				<tbody class="divide-y">
					{#each deliveries as d (d.id)}
						<tr>
							<td class="px-3 py-2 whitespace-nowrap tabular-nums"
								>{d.createTime ? timeFormat.format(timestampDate(d.createTime)) : '–'}</td
							>
							<td class="px-3 py-2">{nameOf(d.channelId)}</td>
							<td class="px-3 py-2">{d.test ? m.deliveries_test() : d.kind}</td>
							<td class="px-3 py-2 break-all">{d.ruleId || '–'}</td>
							<td class="px-3 py-2 whitespace-nowrap">{statusText(d.status)} ({d.attempts})</td>
							<td class="px-3 py-2 break-all text-muted-foreground">{d.error || '–'}</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
		{#if more}
			<Button variant="outline" class="self-start" onclick={() => load(true)}
				>{m.deliveries_more()}</Button
			>
		{/if}
	{/if}
</div>
