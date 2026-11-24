import { create } from '@bufbuild/protobuf';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { Code } from '#lib/api/client.js';
import { adminApi } from '#lib/api/admin.js';
import { NodeGroupSchema, Role } from '#lib/api/gen/sermon/web/v1/access_pb.js';
import { ListOidcMappingsResponseSchema } from '#lib/api/gen/sermon/web/v1/oidc_mapping_pb.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { stepUpState } from '#lib/stores/stepup.svelte.js';
import { rpcError } from '#lib/test/fakes.js';
import OidcMappingsPanel from './OidcMappingsPanel.svelte';

const groups = [
	create(NodeGroupSchema, { id: '0193f5a0-0000-7000-8000-0000000000c1', name: 'web' })
];
const hostile = '<img src=x onerror=alert(1)>';

function listing(extra: Partial<{ oidc: boolean; provisioning: boolean }> = {}) {
	return create(ListOidcMappingsResponseSchema, {
		oidc: true,
		provisioning: true,
		mappings: [
			{ id: 'm1', oidcGroup: 'ops', role: Role.OPERATOR, groupId: groups[0].id },
			{ id: 'm2', oidcGroup: hostile, role: Role.ADMIN }
		],
		...extra
	});
}

describe('OidcMappingsPanel', () => {
	beforeEach(() => switchLocale('en'));
	afterEach(() => {
		vi.restoreAllMocks();
		if (stepUpState.open) stepUpState.finish(false);
	});

	it('lists the mappings as text with role and scope', async () => {
		vi.spyOn(adminApi.oidcMappings, 'listOidcMappings').mockResolvedValue(listing());
		render(OidcMappingsPanel, { groups });
		const rows = page.getByRole('row');
		await expect.element(rows.nth(1)).toMatchTextContent(/ops\s*Operator\s*web/);
		await expect
			.element(rows.nth(2))
			.toMatchTextContent(/<img src=x onerror=alert\(1\)>\s*Admin\s*All groups/);
		expect(document.querySelector('img')).toBeNull();
		await expect.element(page.getByText(/get an account and become members/)).toBeVisible();
	});

	it('creates and deletes mappings', async () => {
		const list = vi.spyOn(adminApi.oidcMappings, 'listOidcMappings').mockResolvedValue(listing());
		const createM = vi
			.spyOn(adminApi.oidcMappings, 'createOidcMapping')
			.mockResolvedValue({} as never);
		const del = vi.spyOn(adminApi.oidcMappings, 'deleteOidcMapping').mockResolvedValue({} as never);
		render(OidcMappingsPanel, { groups });
		await page.getByLabelText('Group at the provider').fill('  dba  ');
		await page.getByLabelText('Role', { exact: true }).selectOptions('Admin');
		await page.getByLabelText('Applies to').selectOptions('web');
		await page.getByRole('button', { name: 'Create' }).click();
		await vi.waitFor(() =>
			expect(createM).toHaveBeenCalledWith({
				oidcGroup: 'dba',
				groupId: groups[0].id,
				role: Role.ADMIN
			})
		);
		await expect.element(page.getByLabelText('Group at the provider')).toHaveValue('');
		await page.getByRole('button', { name: 'Delete the mapping of ops' }).click();
		await page
			.getByRole('button', { name: /Really delete|Delete/ })
			.first()
			.click();
		await vi.waitFor(() => expect(del).toHaveBeenCalledWith({ id: 'm1' }));
		expect(list.mock.calls.length).toBeGreaterThanOrEqual(3);
	});

	it('explains a missing provider', async () => {
		vi.spyOn(adminApi.oidcMappings, 'listOidcMappings').mockResolvedValue(
			listing({ oidc: false, provisioning: false })
		);
		render(OidcMappingsPanel, { groups });
		await expect.element(page.getByText(/has no OIDC provider/)).toBeVisible();
	});

	it('explains switched-off provisioning and refusals', async () => {
		vi.spyOn(adminApi.oidcMappings, 'listOidcMappings').mockResolvedValue(
			listing({ provisioning: false })
		);
		vi.spyOn(adminApi.oidcMappings, 'createOidcMapping').mockRejectedValue(
			rpcError(Code.AlreadyExists)
		);
		render(OidcMappingsPanel, { groups });
		await expect.element(page.getByText(/creates no accounts/)).toBeVisible();
		await page.getByLabelText('Group at the provider').fill('ops');
		await page.getByRole('button', { name: 'Create' }).click();
		await expect.element(page.getByRole('alert')).toBeVisible();
	});
});
