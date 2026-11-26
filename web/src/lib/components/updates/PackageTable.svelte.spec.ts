import { create } from '@bufbuild/protobuf';
import { beforeEach, describe, expect, it } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { UpdatePackageSchema } from '#lib/api/gen/sermon/web/v1/update_pb.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import PackageTable from './PackageTable.svelte';

describe('PackageTable', () => {
	beforeEach(() => switchLocale('en'));

	// Review M4-30: a hostile package source can't make two packages meet in one key (and break
	// the list): "a b" with "c" and "a" with "b c".
	it('lists packages whose names and versions join alike', async () => {
		render(PackageTable, {
			caption: 'Available updates',
			packages: [
				create(UpdatePackageSchema, { name: 'a b', candidateVersion: 'c' }),
				create(UpdatePackageSchema, { name: 'a', candidateVersion: 'b c' })
			]
		});
		await expect.element(page.getByRole('row').nth(2)).toBeVisible();
		expect(page.getByRole('row').elements()).toHaveLength(3);
	});
});
