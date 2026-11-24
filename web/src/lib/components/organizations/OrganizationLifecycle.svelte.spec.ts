import { create } from '@bufbuild/protobuf';
import { timestampFromDate } from '@bufbuild/protobuf/wkt';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { Code } from '#lib/api/client.js';
import { adminApi } from '#lib/api/admin.js';
import {
	OrganizationStatus,
	OrganizationSummarySchema
} from '#lib/api/gen/sermon/web/v1/organization_pb.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { m } from '#lib/paraglide/messages.js';
import { rpcError } from '#lib/test/fakes.js';
import OrganizationLifecycle from './OrganizationLifecycle.svelte';

const id = '0193f5a0-0000-7000-8000-0000000001bb';
const active = create(OrganizationSummarySchema, {
	id,
	name: 'Beta AG',
	status: OrganizationStatus.ACTIVE
});
const locked = create(OrganizationSummarySchema, {
	id,
	name: 'Beta AG',
	status: OrganizationStatus.LOCKED,
	lockTime: timestampFromDate(new Date('2026-11-20T10:00:00Z'))
});

describe('OrganizationLifecycle', () => {
	beforeEach(() => switchLocale('en'));
	afterEach(() => vi.restoreAllMocks());

	// M2-33, ADR 0029: a destructive action explains itself and needs the exact name.
	it('locks only after the exact name is typed', async () => {
		const lock = vi.spyOn(adminApi.organization, 'lockOrganization').mockResolvedValue({} as never);
		const onDone = vi.fn();
		render(OrganizationLifecycle, { organization: active, onDone });

		await expect.element(page.getByRole('button', { name: 'Delete now' })).not.toBeInTheDocument();
		await page.getByRole('button', { name: 'Lock' }).click();
		await expect.element(page.getByText(/Members are signed out at once/)).toBeVisible();
		const confirm = page.getByRole('button', { name: 'Lock', exact: true }).last();
		await page.getByLabelText('Type the name “Beta AG” to confirm').fill('beta ag');
		await expect.element(confirm).toBeDisabled();
		await page.getByLabelText('Type the name “Beta AG” to confirm').fill('Beta AG');
		await confirm.click();

		await vi.waitFor(() =>
			expect(onDone).toHaveBeenCalledWith(m.orgs_done_lock({ name: 'Beta AG' }))
		);
		expect(lock).toHaveBeenCalledWith(
			expect.objectContaining({ organizationId: id, confirmName: 'Beta AG' })
		);
	});

	it('deletes a locked organization and reports the removed accounts', async () => {
		vi.spyOn(adminApi.organization, 'deleteOrganization').mockResolvedValue({
			accountsDeleted: 3n
		} as never);
		const onDone = vi.fn();
		render(OrganizationLifecycle, { organization: locked, onDone });

		await page.getByRole('button', { name: 'Delete now' }).click();
		await expect
			.element(page.getByText(/Deletes the organization at once and for good/))
			.toBeVisible();
		await page.getByLabelText('Type the name “Beta AG” to confirm').fill('Beta AG');
		await page.getByRole('button', { name: 'Delete now' }).last().click();

		await vi.waitFor(() =>
			expect(onDone).toHaveBeenCalledWith(m.orgs_done_delete({ name: 'Beta AG', accounts: '3' }))
		);
	});

	// M3-28: right after the lock the server waits 10 minutes; the dialog says why.
	it('explains that deleting at once waits for 10 minutes after the lock', async () => {
		vi.spyOn(adminApi.organization, 'deleteOrganization').mockRejectedValue(
			rpcError(Code.FailedPrecondition)
		);
		const fresh = create(OrganizationSummarySchema, {
			...locked,
			lockTime: timestampFromDate(new Date(Date.now() - 60_000))
		});
		render(OrganizationLifecycle, { organization: fresh, onDone: vi.fn() });
		await page.getByRole('button', { name: 'Delete now' }).click();
		await page.getByLabelText('Type the name “Beta AG” to confirm').fill('Beta AG');
		await page.getByRole('button', { name: 'Delete now' }).last().click();
		await expect.element(page.getByText(/locked just now/)).toBeVisible();
	});

	it('reports a name the server refuses and an organization that is no longer locked', async () => {
		vi.spyOn(adminApi.organization, 'unlockOrganization')
			.mockRejectedValueOnce(rpcError(Code.InvalidArgument))
			.mockRejectedValueOnce(rpcError(Code.FailedPrecondition));
		const onDone = vi.fn();
		render(OrganizationLifecycle, { organization: locked, onDone });

		await page.getByRole('button', { name: 'Lift lock' }).click();
		await page.getByLabelText('Type the name “Beta AG” to confirm').fill('Beta AG');
		await page.getByRole('button', { name: 'Lift lock' }).last().click();
		await expect.element(page.getByRole('alert')).toHaveTextContent(m.orgs_error_name());
		await page.getByRole('button', { name: 'Lift lock' }).last().click();
		await expect.element(page.getByRole('alert')).toHaveTextContent(m.orgs_error_not_locked());
		expect(onDone).not.toHaveBeenCalled();
	});

	it('downloads the audit log and says whether the chain holds', async () => {
		vi.spyOn(adminApi.organization, 'exportOrganizationAudit').mockResolvedValue({
			fileName: `audit-${id}.jsonl`,
			content: new TextEncoder().encode('{}\n'),
			chainValid: false
		} as never);
		const blob = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:x');
		vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {});
		const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
		render(OrganizationLifecycle, { organization: locked, onDone: vi.fn() });

		await page.getByRole('button', { name: 'Download audit log' }).click();
		await expect.element(page.getByRole('status')).toHaveTextContent(m.orgs_export_broken());
		expect(blob).toHaveBeenCalledOnce();
		expect(click).toHaveBeenCalledOnce();
	});
});
