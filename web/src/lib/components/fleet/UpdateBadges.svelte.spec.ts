import { create } from '@bufbuild/protobuf';
import { beforeEach, describe, expect, it } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { NodeSummarySchema } from '#lib/api/gen/sermon/web/v1/fleet_pb.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import UpdateBadges from './UpdateBadges.svelte';

describe('UpdateBadges', () => {
	beforeEach(() => switchLocale('en'));

	it('names security updates and a needed reboot in words', async () => {
		render(UpdateBadges, {
			node: create(NodeSummarySchema, { securityUpdates: 3, rebootRequired: true })
		});
		await expect.element(page.getByText('3 security')).toBeVisible();
		await expect.element(page.getByText('reboot needed')).toBeVisible();
	});

	it('shows nothing for a node without either', async () => {
		const { container } = await render(UpdateBadges, {
			node: create(NodeSummarySchema, { updates: 4, securityUpdates: 0 })
		});
		expect(container.textContent?.trim()).toBe('');
	});
});
