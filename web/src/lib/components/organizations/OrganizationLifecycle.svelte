<script lang="ts">
	import { saveFile } from '#lib/download/download.js';
	import { timestampDate } from '@bufbuild/protobuf/wkt';
	import { Code, codeOf } from '#lib/api/client.js';
	import type { OrganizationSummary } from '#lib/api/gen/sermon/web/v1/organization_pb.js';
	import { adminApi } from '#lib/api/admin.js';
	import { adminError } from '#lib/admin/admin.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { localeState } from '#lib/i18n/locale.svelte.js';
	import { type LifecycleAction, actionsFor, confirmed } from '#lib/organizations/lifecycle.js';
	import { m } from '#lib/paraglide/messages.js';
	import { withStepUp } from '#lib/stores/stepup.svelte.js';

	/**
	 * Lock, schedule the deletion, lift the lock, delete at once and export the audit log of an
	 * organization (M2-33, ADR 0029), for the superadmin. Each change explains itself, asks for the
	 * exact name and a step-up; the server checks everything again. onDone gets the notice after a
	 * change.
	 */
	let {
		organization,
		onDone
	}: { organization: OrganizationSummary; onDone: (notice: string) => void } = $props();

	type Change = Exclude<LifecycleAction, 'export'>;

	let chosen = $state<Change | undefined>();
	let typed = $state('');
	let failure = $state('');
	let exported = $state('');
	let busy = $state(false);
	const actions = $derived(actionsFor(organization.status));
	const name = $derived(organization.name);

	const labels: Record<LifecycleAction, () => string> = {
		lock: m.orgs_action_lock,
		schedule: m.orgs_action_schedule,
		unlock: m.orgs_action_unlock,
		delete: m.orgs_action_delete,
		export: m.orgs_action_export
	};
	const explanations: Record<Change, () => string> = {
		lock: m.orgs_explain_lock,
		schedule: m.orgs_explain_schedule,
		unlock: m.orgs_explain_unlock,
		delete: m.orgs_explain_delete
	};

	function choose(action: LifecycleAction) {
		failure = '';
		exported = '';
		if (action === 'export') {
			void exportAudit();
			return;
		}
		chosen = action;
		typed = '';
	}

	function failed(error: unknown): string {
		if (codeOf(error) === Code.InvalidArgument) return m.orgs_error_name();
		return adminError(error, m.orgs_error_not_locked());
	}

	async function change(action: Change): Promise<string> {
		const req = { organizationId: organization.id, confirmName: typed.trim() };
		switch (action) {
			case 'lock':
				await adminApi.organization.lockOrganization(req);
				return m.orgs_done_lock({ name });
			case 'schedule': {
				const res = await adminApi.organization.scheduleOrganizationDeletion(req);
				const date = res.deletionTime
					? timestampDate(res.deletionTime).toLocaleDateString(localeState.current)
					: '–';
				return m.orgs_done_schedule({ name, date });
			}
			case 'unlock':
				await adminApi.organization.unlockOrganization(req);
				return m.orgs_done_unlock({ name });
			case 'delete': {
				const res = await adminApi.organization.deleteOrganization(req);
				return m.orgs_done_delete({ name, accounts: String(res.accountsDeleted) });
			}
		}
	}

	async function submit(event: SubmitEvent) {
		event.preventDefault();
		if (!chosen || !confirmed(name, typed)) return;
		const action = chosen;
		failure = '';
		busy = true;
		try {
			onDone(await withStepUp(() => change(action)));
		} catch (error) {
			failure = failed(error);
		} finally {
			busy = false;
		}
	}

	async function exportAudit() {
		busy = true;
		try {
			const res = await withStepUp(() =>
				adminApi.organization.exportOrganizationAudit({ organizationId: organization.id })
			);
			saveFile(res.content as Uint8Array<ArrayBuffer>, res.fileName, 'application/x-ndjson');
			exported = res.chainValid ? m.orgs_export_done() : m.orgs_export_broken();
		} catch (error) {
			failure = failed(error);
		} finally {
			busy = false;
		}
	}
</script>

<section
	class="flex flex-col gap-4 rounded-lg border p-4"
	aria-label={m.orgs_lifecycle_label({ name })}
>
	{#if failure}
		<p role="alert" class="rounded-md border border-destructive/50 p-3 text-sm text-destructive">
			{failure}
		</p>
	{/if}
	{#if exported}
		<p role="status" class="text-sm">{exported}</p>
	{/if}
	<div class="flex flex-wrap gap-2">
		{#each actions as action (action)}
			<Button
				type="button"
				size="sm"
				variant={action === 'delete' ? 'destructive' : 'outline'}
				aria-pressed={chosen === action}
				disabled={busy}
				onclick={() => choose(action)}
			>
				{labels[action]()}
			</Button>
		{/each}
	</div>
	{#if chosen}
		<form class="flex flex-col gap-3" onsubmit={submit}>
			<p class="max-w-prose text-sm">{explanations[chosen]()}</p>
			<div class="flex flex-col gap-1">
				<Label for="org-confirm-name">{m.orgs_confirm_name({ name })}</Label>
				<Input
					id="org-confirm-name"
					class="max-w-sm"
					autocomplete="off"
					spellcheck={false}
					bind:value={typed}
					maxlength={200}
				/>
			</div>
			<div class="flex gap-2">
				<Button
					type="submit"
					variant={chosen === 'delete' ? 'destructive' : 'default'}
					disabled={busy || !confirmed(name, typed)}
				>
					{labels[chosen]()}
				</Button>
				<Button type="button" variant="ghost" onclick={() => (chosen = undefined)}>
					{m.stepup_cancel()}
				</Button>
			</div>
		</form>
	{/if}
</section>
