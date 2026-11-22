import { create } from '@bufbuild/protobuf';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { Code } from '#lib/api/client.js';
import { adminApi } from '#lib/api/admin.js';
import { NodeGroupSchema } from '#lib/api/gen/sermon/web/v1/access_pb.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { m } from '#lib/paraglide/messages.js';
import { rpcError } from '#lib/test/fakes.js';
import GroupsPanel from './GroupsPanel.svelte';

const groups = [create(NodeGroupSchema, { id: 'g1', name: 'web' })];

describe('GroupsPanel', () => {
	beforeEach(() => switchLocale('en'));
	afterEach(() => vi.restoreAllMocks());

	it('creates a group and reports a taken name', async () => {
		const createGroup = vi
			.spyOn(adminApi.access, 'createNodeGroup')
			.mockRejectedValueOnce(rpcError(Code.AlreadyExists))
			.mockResolvedValueOnce({} as never);
		const onChange = vi.fn().mockResolvedValue(undefined);
		render(GroupsPanel, { groups, onChange });

		await page.getByLabelText('New group').fill(' web ');
		await page.getByRole('button', { name: 'Create' }).click();
		await expect.element(page.getByRole('alert')).toHaveTextContent(m.admin_error_exists());
		await page.getByLabelText('New group').fill('db');
		await page.getByRole('button', { name: 'Create' }).click();

		await vi.waitFor(() => expect(onChange).toHaveBeenCalledOnce());
		expect(createGroup.mock.calls.map((c) => c[0])).toEqual([{ name: 'web' }, { name: 'db' }]);
		await expect.element(page.getByLabelText('New group')).toHaveValue('');
	});

	it('renames and deletes after a second click', async () => {
		const rename = vi.spyOn(adminApi.access, 'renameNodeGroup').mockResolvedValue({} as never);
		const remove = vi.spyOn(adminApi.access, 'deleteNodeGroup').mockResolvedValue({} as never);
		render(GroupsPanel, { groups, onChange: vi.fn().mockResolvedValue(undefined) });

		await page.getByRole('button', { name: 'Rename' }).click();
		await page.getByLabelText('Group name').fill('frontend');
		await page.getByRole('button', { name: 'Save' }).click();
		await vi.waitFor(() =>
			expect(rename).toHaveBeenCalledWith({ groupId: 'g1', name: 'frontend' })
		);

		await page.getByRole('button', { name: 'Delete', exact: true }).click();
		expect(remove).not.toHaveBeenCalled();
		await page.getByRole('button', { name: 'Really delete' }).click();
		await vi.waitFor(() => expect(remove).toHaveBeenCalledWith({ groupId: 'g1' }));
	});
});
