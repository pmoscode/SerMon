<script lang="ts">
	import { api, Code, codeOf } from '#lib/api/client.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { m } from '#lib/paraglide/messages.js';

	/** Minimum password length of the server's policy (KONZEPT §7). */
	const MIN_PASSWORD = 12;

	let { onDone }: { onDone: () => void } = $props();

	let values = $state({
		token: '',
		organization: '',
		name: '',
		email: '',
		password: '',
		repeat: ''
	});
	let submitted = $state(false);
	let busy = $state(false);
	let failure = $state('');

	const problems = $derived({
		token: !values.token.trim() ? m.field_required() : '',
		organization: !values.organization.trim() ? m.field_required() : '',
		name: !values.name.trim() ? m.field_required() : '',
		email: !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(values.email.trim()) ? m.field_email_invalid() : '',
		password:
			values.password.length < MIN_PASSWORD
				? m.field_password_too_short({ min: String(MIN_PASSWORD) })
				: '',
		repeat: values.repeat !== values.password ? m.setup_error_repeat() : ''
	});
	const valid = $derived(Object.values(problems).every((p) => !p));

	async function submit(event: SubmitEvent) {
		event.preventDefault();
		submitted = true;
		failure = '';
		if (!valid) return;
		busy = true;
		try {
			await api.setup.completeSetup({
				token: values.token.trim(),
				organizationName: values.organization.trim(),
				displayName: values.name.trim(),
				email: values.email.trim(),
				password: values.password
			});
			onDone();
		} catch (error) {
			switch (codeOf(error)) {
				case Code.Unauthenticated:
					failure = m.setup_error_token();
					break;
				case Code.InvalidArgument:
					failure = m.setup_error_invalid();
					break;
				case Code.ResourceExhausted:
					failure = m.login_error_rate_limited();
					break;
				default:
					failure = m.error_unavailable();
			}
		} finally {
			busy = false;
		}
	}

	const fields = [
		{ key: 'token', type: 'text', autocomplete: 'off', label: () => m.setup_token() },
		{
			key: 'organization',
			type: 'text',
			autocomplete: 'organization',
			label: () => m.setup_organization()
		},
		{ key: 'name', type: 'text', autocomplete: 'name', label: () => m.field_name() },
		{ key: 'email', type: 'email', autocomplete: 'username', label: () => m.field_email() },
		{
			key: 'password',
			type: 'password',
			autocomplete: 'new-password',
			label: () => m.field_password_label()
		},
		{
			key: 'repeat',
			type: 'password',
			autocomplete: 'new-password',
			label: () => m.field_password_repeat()
		}
	] as const;
</script>

<form class="flex flex-col gap-4" novalidate onsubmit={submit}>
	{#if failure}
		<p role="alert" class="rounded-md border border-destructive/50 p-3 text-sm text-destructive">
			{failure}
		</p>
	{/if}
	{#each fields as field (field.key)}
		{@const problem = submitted ? problems[field.key] : ''}
		<div class="flex flex-col gap-2">
			<Label for="setup-{field.key}">{field.label()}</Label>
			<Input
				id="setup-{field.key}"
				type={field.type}
				autocomplete={field.autocomplete}
				bind:value={values[field.key]}
				aria-invalid={!!problem}
				aria-describedby={problem ? `setup-${field.key}-error` : undefined}
			/>
			{#if field.key === 'token' && !problem}
				<p class="text-sm text-muted-foreground">{m.setup_token_hint()}</p>
			{/if}
			{#if problem}
				<p id="setup-{field.key}-error" class="text-sm text-destructive">{problem}</p>
			{/if}
		</div>
	{/each}
	<Button type="submit" disabled={busy}>{busy ? m.setup_busy() : m.setup_submit()}</Button>
</form>
