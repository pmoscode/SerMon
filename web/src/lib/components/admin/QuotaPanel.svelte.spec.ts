import { create } from '@bufbuild/protobuf';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { adminApi } from '#lib/api/admin.js';
import { GetOrganizationQuotasResponseSchema } from '#lib/api/gen/sermon/web/v1/access_pb.js';
import { OrganizationQuotasSchema } from '#lib/api/gen/sermon/web/v1/organization_pb.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import QuotaPanel from './QuotaPanel.svelte';

describe('QuotaPanel', () => {
	beforeEach(() => switchLocale('en'));
	afterEach(() => vi.restoreAllMocks());

	// M2-32: the admins of the organization see the quotas and their use.
	it('shows quotas, their use and the defaults', async () => {
		vi.spyOn(adminApi.access, 'getOrganizationQuotas').mockResolvedValue(
			create(GetOrganizationQuotasResponseSchema, {
				quotas: create(OrganizationQuotasSchema, { maxNodes: 20, seriesPerNode: 500 }),
				nodes: 12n,
				members: 4n,
				openInvitations: 1n
			})
		);
		render(QuotaPanel);
		const list = page.getByRole('definition');
		await expect.element(list.nth(0)).toHaveTextContent('12 of 20');
		await expect.element(list.nth(1)).toHaveTextContent('5, no limit');
		await expect.element(list.nth(2)).toHaveTextContent('Instance default');
		await expect.element(list.nth(4)).toHaveTextContent('at most 500');
	});
});
