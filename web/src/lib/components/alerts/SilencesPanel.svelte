<script lang="ts">
	import { timestampDate, timestampFromMs } from '@bufbuild/protobuf/wkt';
	import { api } from '#lib/api/client.js';
	import { alertingApi } from '#lib/api/alerting.js';
	import type { NodeSummary } from '#lib/api/gen/sermon/web/v1/fleet_pb.js';
	import type { AlertSeverity } from '#lib/api/gen/sermon/web/v1/notification_policy_pb.js';
	import type { AlertSilence } from '#lib/api/gen/sermon/web/v1/silence_pb.js';
	import MatcherFields from '#lib/components/notify/MatcherFields.svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import {
		coversGroups,
		durationText,
		labelsText,
		parseLabels,
		parsePatterns,
		patternsText,
		silenceDurations
	} from '#lib/notify/forms.js';
	import { notifyError } from '#lib/notify/notify.js';
	import { m } from '#lib/paraglide/messages.js';
	import { sessionState } from '#lib/stores/session.svelte.js';
	import { withStepUp } from '#lib/stores/stepup.svelte.js';
	import { parseTags, tagsText } from '#lib/tags/tags.js';

	export interface SilenceDraft {
		groupIds: string[];
		nodeIds: string[];
		rules: string[];
	}

	/**
	 * Silences (M2-15): operators mute the notifications of their groups for a while. The comment
	 * and the host names are shown as text.
	 */
	let { draft }: { draft?: SilenceDraft } = $props();

	let silences = $state<AlertSilence[]>([]);
	let nodes = $state<NodeSummary[]>([]);
	let status = $state<'loading' | 'ready' | 'error'>('loading');
	let failure = $state('');
	let busy = $state(false);
	let creating = $state(false);

	// The form.
	let severities = $state<AlertSeverity[]>([]);
	let groupIds = $state<string[]>([]);
	let nodeIds = $state<string[]>([]);
	let rules = $state('');
	let labels = $state('');
	let tags = $state('');
	let duration = $state(240);
	let comment = $state('');

	const allowed = $derived(sessionState.current?.silenceGroupIds ?? []);
	const groups = $derived(
		[...new Map(nodes.map((n) => [n.groupId, n.groupName])).entries()]
			.map(([id, name]) => ({ id, name }))
			.filter((g) => coversGroups(allowed, [g.id]))
			.sort((a, b) => a.name.localeCompare(b.name))
	);
	const groupNodes = $derived(nodes.filter((n) => groupIds.includes(n.groupId)));
	const mayCreate = $derived(allowed.length > 0);

	function start(d?: SilenceDraft) {
		severities = [];
		groupIds = [...(d?.groupIds ?? [])];
		nodeIds = [...(d?.nodeIds ?? [])];
		rules = patternsText(d?.rules ?? []);
		labels = '';
		tags = '';
		duration = 240;
		comment = '';
		failure = '';
		creating = true;
	}

	$effect(() => {
		if (draft) start(draft);
	});

	async function load() {
		try {
			const [s, f] = await Promise.all([
				alertingApi.silences.listSilences({}),
				api.fleet.getFleetSummary({})
			]);
			silences = s.silences;
			nodes = f.nodes;
			status = 'ready';
		} catch {
			status = 'error';
		}
	}

	$effect(() => {
		void load();
	});

	async function run(action: () => Promise<unknown>): Promise<boolean> {
		busy = true;
		failure = '';
		try {
			await withStepUp(action);
			await load();
			return true;
		} catch (error) {
			failure = notifyError(error);
			return false;
		} finally {
			busy = false;
		}
	}

	async function create(event: SubmitEvent) {
		event.preventDefault();
		const parsed = parseLabels(labels);
		if ('error' in parsed) {
			failure = m.matchers_labels_invalid({ line: String(parsed.error) });
			return;
		}
		const tagged = parseTags(tags);
		if ('error' in tagged) {
			failure = m.matchers_tags_invalid({ pair: tagged.error });
			return;
		}
		const ok = await run(() =>
			alertingApi.silences.createSilence({
				spec: {
					matchers: {
						severities,
						groupIds,
						rules: parsePatterns(rules),
						labels: parsed.labels,
						tags: tagged.tags
					},
					nodeIds: nodeIds.filter((id) => groupNodes.some((n) => n.nodeId === id)),
					endTime: timestampFromMs(Date.now() + duration * 60_000),
					comment: comment.trim()
				}
			})
		);
		if (ok) creating = false;
	}

	const expire = (s: AlertSilence) =>
		run(() => alertingApi.silences.expireSilence({ silenceId: s.id }));

	const now = $derived(Date.now());
	const timeFormat = $derived(
		new Intl.DateTimeFormat(localeState.current, { dateStyle: 'short', timeStyle: 'short' })
	);
	const ended = (s: AlertSilence) =>
		!!s.spec?.endTime && timestampDate(s.spec.endTime).getTime() <= now;
	const begun = (s: AlertSilence) =>
		!s.spec?.startTime || timestampDate(s.spec.startTime).getTime() <= now;
	const groupName = (id: string) => nodes.find((n) => n.groupId === id)?.groupName ?? '–';
	const hostName = (id: string) => nodes.find((n) => n.nodeId === id)?.hostname ?? id;

	function selector(s: AlertSilence): string {
		const mt = s.spec?.matchers;
		const parts = [];
		if (mt?.groupIds.length)
			parts.push(m.silence_sel_groups({ names: mt.groupIds.map(groupName).join(', ') }));
		else parts.push(m.admin_all_groups());
		if (s.spec?.nodeIds.length)
			parts.push(m.silence_sel_nodes({ names: s.spec.nodeIds.map(hostName).join(', ') }));
		if (mt?.rules.length) parts.push(m.silence_sel_rules({ rules: mt.rules.join(', ') }));
		if (mt && Object.keys(mt.labels).length)
			parts.push(labelsText(mt.labels).replaceAll('\n', ', '));
		if (mt && Object.keys(mt.tags).length) parts.push(tagsText(mt.tags));
		return parts.join(' · ');
	}

	const complete = $derived(
		comment.trim() !== '' &&
			(groupIds.length > 0 || rules.trim() !== '' || labels.trim() !== '' || tags.trim() !== '')
	);
</script>

<div class="flex flex-col gap-4">
	<p class="text-sm text-muted-foreground">{m.silences_intro()}</p>
	{#if status === 'loading'}
		<p role="status" class="text-muted-foreground">{m.loading()}</p>
	{:else if status === 'error'}
		<div class="flex items-center gap-3">
			<p role="alert" class="text-destructive">{m.error_unavailable()}</p>
			<Button variant="outline" onclick={load}>{m.retry()}</Button>
		</div>
	{:else}
		{#if failure}
			<p role="alert" class="rounded-md border border-destructive/50 p-3 text-sm text-destructive">
				{failure}
			</p>
		{/if}
		{#if creating}
			<form
				class="flex flex-col gap-4 rounded-lg border p-4"
				onsubmit={create}
				aria-label={m.silence_new()}
			>
				<MatcherFields
					id="silence"
					{groups}
					bind:severities
					bind:groupIds
					bind:rules
					bind:labels
					bind:tags
				/>
				{#if groupNodes.length > 0}
					<fieldset class="flex flex-wrap gap-3 rounded-md border p-3 text-sm">
						<legend class="px-1 font-medium">{m.silence_nodes()}</legend>
						{#each groupNodes as n (n.nodeId)}
							<label class="flex items-center gap-1">
								<input
									type="checkbox"
									class="size-4"
									checked={nodeIds.includes(n.nodeId)}
									onchange={(e) =>
										(nodeIds = e.currentTarget.checked
											? [...nodeIds, n.nodeId]
											: nodeIds.filter((id) => id !== n.nodeId))}
								/>
								{n.hostname}
							</label>
						{/each}
						<p class="w-full text-xs text-muted-foreground">{m.silence_nodes_hint()}</p>
					</fieldset>
				{/if}
				<div class="flex flex-wrap items-end gap-3">
					<div class="flex flex-col gap-1">
						<Label for="silence-duration">{m.silence_duration()}</Label>
						<select
							id="silence-duration"
							class="h-9 rounded-md border border-input bg-background px-2 text-sm focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
							bind:value={duration}
						>
							{#each silenceDurations as d (d)}<option value={d}>{durationText(d)}</option>{/each}
						</select>
					</div>
					<div class="flex min-w-64 flex-1 flex-col gap-1">
						<Label for="silence-comment">{m.silence_comment()}</Label>
						<textarea
							id="silence-comment"
							rows="2"
							maxlength={500}
							class="rounded-md border border-input bg-background px-2 py-1 text-sm focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
							bind:value={comment}></textarea>
					</div>
				</div>
				<div class="flex gap-2">
					<Button type="submit" disabled={busy || !complete}>{m.silence_create()}</Button>
					<Button type="button" variant="ghost" onclick={() => (creating = false)}
						>{m.notify_cancel()}</Button
					>
				</div>
			</form>
		{:else if mayCreate}
			<Button class="self-start" onclick={() => start()}>{m.silence_new()}</Button>
		{/if}
		<ul class="divide-y rounded-lg border" aria-label={m.silences_title()}>
			{#each silences as s (s.id)}
				<li class="flex flex-wrap items-center justify-between gap-2 px-3 py-3 text-sm">
					<span class="min-w-0">
						<span class="font-medium">
							{ended(s) ? m.silence_ended() : begun(s) ? m.silence_active() : m.silence_upcoming()}
						</span>
						<span class="text-muted-foreground">
							· {s.spec?.startTime ? timeFormat.format(timestampDate(s.spec.startTime)) : ''} – {s
								.spec?.endTime
								? timeFormat.format(timestampDate(s.spec.endTime))
								: ''}
						</span>
						<span class="block break-words">{selector(s)}</span>
						<span class="block text-xs whitespace-pre-line text-muted-foreground"
							>{s.spec?.comment}</span
						>
					</span>
					{#if !ended(s) && coversGroups(allowed, s.spec?.matchers?.groupIds ?? [])}
						<Button size="sm" variant="ghost" disabled={busy} onclick={() => expire(s)}>
							{begun(s) ? m.silence_expire() : m.silence_cancel()}
						</Button>
					{/if}
				</li>
			{:else}
				<li class="px-3 py-4 text-sm text-muted-foreground">{m.silences_none()}</li>
			{/each}
		</ul>
	{/if}
</div>
