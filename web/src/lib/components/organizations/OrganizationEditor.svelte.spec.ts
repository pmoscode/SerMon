import { create } from '@bufbuild/protobuf';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { Code } from '#lib/api/client.js';
import { adminApi } from '#lib/api/admin.js';
import {
	OrganizationQuotasSchema,
	OrganizationSummarySchema
} from '#lib/api/gen/sermon/web/v1/organization_pb.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { m } from '#lib/paraglide/messages.js';
import { rpcError } from '#lib/test/fakes.js';
import OrganizationEditor from './OrganizationEditor.svelte';

const organization = create(OrganizationSummarySchema, {
	id: '0193f5a0-0000-7000-8000-0000000001bb',
	name: 'Beta AG',
	quotas: create(OrganizationQuotasSchema, { maxNodes: 10 })
});

describe('OrganizationEditor', () => {
	beforeEach(() => switchLocale('en'));
	afterEach(() => vi.restoreAllMocks());

	// M2-32: the form starts from the stored quotas; empty fields are the instance default.
	it('saves name and quotas', async () => {
		const update = vi
			.spyOn(adminApi.organization, 'updateOrganization')
			.mockResolvedValue({} as never);
		const onDone = vi.fn();
		render(OrganizationEditor, { organization, onDone });

		await expect.element(page.getByLabelText('Nodes')).toHaveValue('10');
		await page.getByLabelText('Name').fill(' Beta GmbH ');
		await page.getByLabelText('Nodes').fill('');
		await page.getByLabelText('Members (with open invitations)').fill('25');
		await page.getByLabelText('Series per node').fill('500');
		await page.getByRole('button', { name: 'Save' }).click();

		await vi.waitFor(() => expect(onDone).toHaveBeenCalledWith(true));
		const req = update.mock.calls[0]![0] as {
			organizationId: string;
			name: string;
			quotas: { maxNodes?: number; maxMembers?: number; seriesPerNode?: number };
		};
		expect(req.organizationId).toBe(organization.id);
		expect(req.name).toBe('Beta GmbH');
		expect(req.quotas.maxNodes).toBeUndefined();
		expect(req.quotas.maxMembers).toBe(25);
		expect(req.quotas.seriesPerNode).toBe(500);
	});

	it('refuses a quota out of range before asking the server, and reports a taken name', async () => {
		const update = vi
			.spyOn(adminApi.organization, 'updateOrganization')
			.mockRejectedValue(rpcError(Code.AlreadyExists));
		const onDone = vi.fn();
		render(OrganizationEditor, { organization, onDone });

		await page.getByLabelText('Samples per second and node').fill('0');
		await page.getByRole('button', { name: 'Save' }).click();
		await expect
			.element(page.getByRole('alert'))
			.toMatchTextContent(/Samples per second and node: a whole number from 1 to 1000000/);
		expect(update).not.toHaveBeenCalled();

		await page.getByLabelText('Samples per second and node').fill('');
		await page.getByRole('button', { name: 'Save' }).click();
		await expect.element(page.getByRole('alert')).toHaveTextContent(m.admin_error_exists());
		expect(onDone).not.toHaveBeenCalled();
	});
});
