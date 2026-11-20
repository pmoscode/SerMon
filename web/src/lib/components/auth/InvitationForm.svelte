<script lang="ts">
	import { timestampDate } from '@bufbuild/protobuf/wkt';
	import { untrack } from 'svelte';
	import type { GetInvitationResponse } from '#lib/api/gen/sermon/web/v1/invitation_pb.js';
	import { api, Code, codeOf } from '#lib/api/client.js';
	import { roleName } from '#lib/admin/admin.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { formatTimestamp } from '#lib/format/format.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { m } from '#lib/paraglide/messages.js';
	import { sessionState } from '#lib/stores/session.svelte.js';

	/** Accepting an invitation link: a new account, or joining with the logged-in one (ADR 0008). */
	let {
		token,
		onAccepted,
		onJoined
	}: { token: string; onAccepted: () => void; onJoined: () => void } = $props();

	const MIN_PASSWORD = 12;
	let invitation = $state<GetInvitationResponse | undefined>();
	let status = $state<'loading' | 'ready' | 'invalid' | 'unavailable'>('loading');
	let name = $state('');
	let password = $state('');
	let repeat = $state('');
	let submitted = $state(false);
	let busy = $state(false);
	let failure = $state('');

	// Loads once per token; a later login or logout must not reload the invitation.
	$effect(() => {
		const unknown = untrack(() => sessionState.status === 'unknown');
		void Promise.all([
			api.invitation.getInvitation({ token }),
			unknown ? sessionState.load().catch(() => 'anonymous') : undefined
		])
			.then(([res]) => {
				invitation = res;
				status = 'ready';
			})
			.catch((error) => {
				status =
					codeOf(error) === Code.Unauthenticated || codeOf(error) === Code.InvalidArgument
						? 'invalid'
						: 'unavailable';
			});
	});

	const problems = $derived({
		name: !name.trim() ? m.field_required() : '',
		password:
			password.length < MIN_PASSWORD
				? m.field_password_too_short({ min: String(MIN_PASSWORD) })
				: '',
		repeat: repeat !== password ? m.setup_error_repeat() : ''
	});

	async function accept(event: SubmitEvent) {
		event.preventDefault();
		submitted = true;
		failure = '';
		if (Object.values(problems).some(Boolean)) return;
		busy = true;
		try {
			await api.invitation.acceptInvitation({ token, displayName: name.trim(), password });
			onAccepted();
		} catch (error) {
			failure =
				codeOf(error) === Code.Unauthenticated
					? m.invite_error_existing()
					: codeOf(error) === Code.InvalidArgument
						? m.setup_error_invalid()
						: m.error_unavailable();
		} finally {
			busy = false;
		}
	}

	async function join() {
		busy = true;
		failure = '';
		try {
			const res = await api.invitation.joinOrganization({ token });
			await sessionState.switchOrganization(res.organizationId);
			onJoined();
		} catch (error) {
			failure =
				codeOf(error) === Code.Unauthenticated
					? m.invite_error_other_account()
					: m.error_unavailable();
		} finally {
			busy = false;
		}
	}
</script>

{#if status === 'loading'}
	<p role="status" class="text-muted-foreground">{m.loading()}</p>
{:else if status === 'invalid'}
	<p role="alert" class="text-sm text-destructive">{m.invite_invalid()}</p>
{:else if status === 'unavailable' || !invitation}
	<p role="alert" class="text-sm text-destructive">{m.error_unavailable()}</p>
{:else}
	<div class="flex flex-col gap-4">
		<p class="text-sm">
			{m.invite_text({
				org: invitation.organizationName,
				email: invitation.email,
				role: roleName(invitation.role)
			})}
		</p>
		{#if invitation.expireTime}
			<p class="text-xs text-muted-foreground">
				{m.admin_invite_expires({
					when: formatTimestamp(
						timestampDate(invitation.expireTime).getTime() / 1000,
						localeState.current
					)
				})}
			</p>
		{/if}
		{#if failure}
			<p role="alert" class="rounded-md border border-destructive/50 p-3 text-sm text-destructive">
				{failure}
			</p>
		{/if}
		{#if sessionState.status === 'authenticated'}
			<p class="text-sm">{m.invite_logged_in({ email: sessionState.current?.email ?? '' })}</p>
			<Button disabled={busy} onclick={join}>{m.invite_join()}</Button>
		{:else}
			<form class="flex flex-col gap-4" novalidate onsubmit={accept}>
				{#each [{ key: 'name', label: m.field_name(), type: 'text', auto: 'name' }, { key: 'password', label: m.field_password_label(), type: 'password', auto: 'new-password' }, { key: 'repeat', label: m.field_password_repeat(), type: 'password', auto: 'new-password' }] as field (field.key)}
					{@const problem = submitted ? problems[field.key as keyof typeof problems] : ''}
					<div class="flex flex-col gap-2">
						<Label for="invite-{field.key}">{field.label}</Label>
						{#if field.key === 'name'}
							<Input
								id="invite-name"
								autocomplete="name"
								bind:value={name}
								aria-invalid={!!problem}
								aria-describedby={problem ? 'invite-name-error' : undefined}
							/>
						{:else if field.key === 'password'}
							<Input
								id="invite-password"
								type="password"
								autocomplete="new-password"
								bind:value={password}
								aria-invalid={!!problem}
								aria-describedby={problem ? 'invite-password-error' : undefined}
							/>
						{:else}
							<Input
								id="invite-repeat"
								type="password"
								autocomplete="new-password"
								bind:value={repeat}
								aria-invalid={!!problem}
								aria-describedby={problem ? 'invite-repeat-error' : undefined}
							/>
						{/if}
						{#if problem}<p id="invite-{field.key}-error" class="text-sm text-destructive">
								{problem}
							</p>{/if}
					</div>
				{/each}
				<Button type="submit" disabled={busy}>{m.invite_create()}</Button>
			</form>
			<p class="text-xs text-muted-foreground">{m.invite_have_account()}</p>
		{/if}
	</div>
{/if}
