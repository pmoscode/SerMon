<script lang="ts">
	import CircleCheck from '@lucide/svelte/icons/circle-check';
	import Copy from '@lucide/svelte/icons/copy';
	import LoaderCircle from '@lucide/svelte/icons/loader-circle';
	import { resolve } from '$app/paths';
	import type { NodeGroup } from '#lib/api/gen/sermon/web/v1/access_pb.js';
	import type { NodeSummary } from '#lib/api/gen/sermon/web/v1/fleet_pb.js';
	import { api } from '#lib/api/client.js';
	import { adminError, copyText } from '#lib/admin/admin.js';
	import {
		installCommand,
		normalizeFingerprint,
		SCRIPT_DIGEST_HEADER
	} from '#lib/admin/install.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { m } from '#lib/paraglide/messages.js';
	import { sessionState } from '#lib/stores/session.svelte.js';
	import { withStepUp } from '#lib/stores/stepup.svelte.js';

	/** How often the wizard looks for the new node. */
	const POLL = 3000;
	const validities = [
		{ seconds: 3600, label: () => m.addnode_valid_1h() },
		{ seconds: 86400, label: () => m.addnode_valid_24h() },
		{ seconds: 604800, label: () => m.addnode_valid_7d() }
	];

	let groups = $state<NodeGroup[]>([]);
	let groupsLoaded = $state(false);
	let groupId = $state('');
	let ttl = $state(3600);
	let uses = $state(1);
	let secret = $state('');
	let failure = $state('');
	let busy = $state(false);
	let copied = $state(false);
	let known = $state<Set<string>>(new Set());
	let arrived = $state<NodeSummary | undefined>();
	let selfSigned = $state(false);
	let fingerprintText = $state('');
	let digest = $state('');
	let digestFailed = $state(false);

	const allowed = $derived(sessionState.current?.enrollGroupIds ?? []);
	const origin = location.origin;
	const fingerprint = $derived(normalizeFingerprint(fingerprintText));
	const fingerprintInvalid = $derived(selfSigned && fingerprintText.trim() !== '' && !fingerprint);
	/** Empty while the self-signed variant still lacks the fingerprint or the checksum. */
	const command = $derived.by(() => {
		if (!secret) return '';
		if (!selfSigned) return installCommand({ origin, secret });
		if (!fingerprint || !digest) return '';
		return installCommand({ origin, secret, selfSigned: { digest, fingerprint } });
	});

	// The checksum comes over the browser's connection to the server – the channel the admin
	// already trusts; on the host curl can't verify the certificate, sha256sum checks the script.
	$effect(() => {
		if (!selfSigned || digest) return;
		digestFailed = false;
		void fetch('/install.sh', { method: 'HEAD', cache: 'no-store', credentials: 'same-origin' })
			.then((res) => {
				const value = res.ok ? (res.headers.get(SCRIPT_DIGEST_HEADER) ?? '') : '';
				if (/^[0-9a-f]{64}$/.test(value)) digest = value;
				else digestFailed = true;
			})
			.catch(() => (digestFailed = true));
	});

	$effect(() => {
		void api.access
			.listNodeGroups({})
			.then((res) => {
				groups = res.groups.filter((g) => allowed.includes('*') || allowed.includes(g.id));
				groupId ||= groups[0]?.id ?? '';
				groupsLoaded = true;
			})
			.catch((error) => (failure = adminError(error)));
	});

	async function nodes(): Promise<NodeSummary[]> {
		return (await api.fleet.getFleetSummary({ groupId })).nodes;
	}

	async function create(event: SubmitEvent) {
		event.preventDefault();
		busy = true;
		failure = '';
		try {
			known = new Set((await nodes()).map((n) => n.nodeId));
			const res = await withStepUp(() =>
				api.enrollment.createEnrollmentToken({ groupId, ttlSeconds: ttl, maxUses: uses })
			);
			secret = res.secret;
		} catch (error) {
			failure = adminError(error);
		} finally {
			busy = false;
		}
	}

	// After the token: watch the group for a new node until it delivers metrics.
	$effect(() => {
		if (!secret || arrived?.figures?.cpuBusyRatio !== undefined) return;
		const timer = setInterval(async () => {
			try {
				arrived = (await nodes()).find((n) => !known.has(n.nodeId)) ?? arrived;
			} catch {
				// The next round tries again.
			}
		}, POLL);
		return () => clearInterval(timer);
	});

	const connected = $derived(arrived?.online ?? false);
	const delivering = $derived(arrived?.figures?.cpuBusyRatio !== undefined);
</script>

<div class="flex max-w-3xl flex-col gap-6">
	{#if failure}
		<p role="alert" class="rounded-md border border-destructive/50 p-3 text-sm text-destructive">
			{failure}
		</p>
	{/if}
	{#if allowed.length === 0}
		<p role="alert" class="text-sm">{m.addnode_forbidden()}</p>
	{:else if groupsLoaded && groups.length === 0 && !secret}
		<div class="flex flex-col items-start gap-3 rounded-lg border border-dashed p-6">
			<p class="text-sm">{m.addnode_no_groups()}</p>
			{#if sessionState.current?.orgAdmin}
				<Button href={resolve('/(app)/admin')} variant="outline">{m.addnode_create_group()}</Button>
			{/if}
		</div>
	{:else if !secret}
		<form class="flex flex-col gap-4" onsubmit={create}>
			<div class="flex flex-wrap gap-4">
				<div class="flex flex-col gap-1">
					<Label for="addnode-group">{m.fleet_group()}</Label>
					<select
						id="addnode-group"
						class="h-9 rounded-md border border-input bg-background px-2 text-sm"
						bind:value={groupId}
					>
						{#each groups as g (g.id)}
							<option value={g.id}>{g.name}</option>
						{/each}
					</select>
				</div>
				<div class="flex flex-col gap-1">
					<Label for="addnode-ttl">{m.addnode_valid()}</Label>
					<select
						id="addnode-ttl"
						class="h-9 rounded-md border border-input bg-background px-2 text-sm"
						bind:value={ttl}
					>
						{#each validities as v (v.seconds)}
							<option value={v.seconds}>{v.label()}</option>
						{/each}
					</select>
				</div>
				<div class="flex flex-col gap-1">
					<Label for="addnode-uses">{m.addnode_uses()}</Label>
					<Input
						id="addnode-uses"
						type="number"
						min={1}
						max={1000}
						class="w-28"
						bind:value={uses}
					/>
				</div>
			</div>
			<p class="text-sm text-muted-foreground">{m.addnode_uses_hint()}</p>
			<Button type="submit" class="self-start" disabled={busy || !groupId}
				>{m.addnode_create()}</Button
			>
		</form>
	{:else}
		<section class="flex flex-col gap-3" aria-labelledby="addnode-command">
			<h2 id="addnode-command" class="text-lg font-medium">{m.addnode_run()}</h2>
			<p class="text-sm text-muted-foreground">{m.addnode_run_text()}</p>
			<label class="flex items-center gap-2 text-sm">
				<input type="checkbox" bind:checked={selfSigned} class="size-4 accent-primary" />
				{m.addnode_self_signed()}
			</label>
			{#if selfSigned}
				<div class="flex flex-col gap-1">
					<Label for="addnode-fingerprint">{m.addnode_fingerprint()}</Label>
					<Input
						id="addnode-fingerprint"
						class="font-mono text-xs"
						autocomplete="off"
						spellcheck={false}
						aria-invalid={fingerprintInvalid}
						aria-describedby={fingerprintInvalid
							? 'addnode-fingerprint-error addnode-fingerprint-hint'
							: 'addnode-fingerprint-hint'}
						bind:value={fingerprintText}
					/>
					{#if fingerprintInvalid}
						<p id="addnode-fingerprint-error" class="text-sm text-destructive">
							{m.addnode_fingerprint_invalid()}
						</p>
					{/if}
					<p id="addnode-fingerprint-hint" class="text-xs text-muted-foreground">
						{m.addnode_fingerprint_hint()}
					</p>
					{#if digestFailed}
						<p role="alert" class="text-sm text-destructive">{m.addnode_digest_failed()}</p>
					{/if}
				</div>
			{/if}
			{#if command}
				<pre
					class="overflow-x-auto rounded-md bg-muted p-3 font-mono text-xs whitespace-pre-wrap select-all">{command}</pre>
			{/if}
			<div class="flex flex-wrap items-center gap-2">
				<Button
					variant="outline"
					disabled={!command}
					onclick={async () => (copied = await copyText(command))}
				>
					<Copy aria-hidden="true" />
					{copied ? m.security_codes_copied() : m.security_codes_copy()}
				</Button>
				<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- a file of the server, not a page of the app -->
				<a
					href="/install.sh"
					target="_blank"
					rel="external noopener"
					class="text-sm underline underline-offset-2"
				>
					{m.addnode_view_script()}
				</a>
			</div>
			<p class="rounded-md border border-amber-500/50 p-3 text-sm">{m.addnode_secret_once()}</p>
		</section>
		<section class="flex flex-col gap-2" aria-labelledby="addnode-status" aria-live="polite">
			<h2 id="addnode-status" class="text-lg font-medium">{m.addnode_status()}</h2>
			<ul class="flex flex-col gap-1 text-sm">
				<li class="flex items-center gap-2">
					{#if connected}<CircleCheck
							class="size-4 text-emerald-600"
							aria-hidden="true"
						/>{:else}<LoaderCircle
							class="size-4 animate-spin text-muted-foreground"
							aria-hidden="true"
						/>{/if}
					{connected ? m.addnode_connected({ host: arrived?.hostname ?? '' }) : m.addnode_waiting()}
				</li>
				<li class="flex items-center gap-2">
					{#if delivering}<CircleCheck
							class="size-4 text-emerald-600"
							aria-hidden="true"
						/>{:else}<LoaderCircle
							class="size-4 animate-spin text-muted-foreground"
							aria-hidden="true"
						/>{/if}
					{delivering ? m.addnode_metrics() : m.addnode_metrics_waiting()}
				</li>
			</ul>
			{#if arrived}
				<Button href={resolve('/(app)/nodes/[id]', { id: arrived.nodeId })} class="self-start"
					>{m.addnode_open()}</Button
				>
			{/if}
		</section>
	{/if}
</div>
