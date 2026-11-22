import { create } from '@bufbuild/protobuf';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { adminApi } from '#lib/api/admin.js';
import { alertingApi } from '#lib/api/alerting.js';
import { ListChannelsResponseSchema } from '#lib/api/gen/sermon/web/v1/notification_pb.js';
import {
	AlertSeverity,
	ListPoliciesResponseSchema,
	PolicyGrouping
} from '#lib/api/gen/sermon/web/v1/notification_policy_pb.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import PoliciesPanel from './PoliciesPanel.svelte';

describe('PoliciesPanel', () => {
	beforeEach(() => {
		switchLocale('en');
		vi.spyOn(alertingApi.channels, 'listChannels').mockResolvedValue(
			create(ListChannelsResponseSchema, {
				channels: [
					{ id: 'c1', name: 'Ops' },
					{ id: 'c2', name: 'On call' }
				]
			})
		);
		vi.spyOn(adminApi.access, 'listNodeGroups').mockResolvedValue({
			groups: [{ id: 'g1', name: 'web' }]
		} as never);
	});
	afterEach(() => vi.restoreAllMocks());

	it('explains the fallback and creates a policy with matchers and escalation', async () => {
		vi.spyOn(alertingApi.policies, 'listPolicies').mockResolvedValue(
			create(ListPoliciesResponseSchema, {})
		);
		const createPolicy = vi
			.spyOn(alertingApi.policies, 'createPolicy')
			.mockResolvedValue({} as never);
		render(PoliciesPanel);

		await expect
			.element(page.getByRole('note'))
			.toMatchTextContent(/all alerts go to all enabled channels/);
		await page.getByRole('button', { name: 'New policy' }).click();
		await page.getByLabelText('Name').fill('Security');
		await page.getByRole('checkbox', { name: 'Ops' }).first().click();
		await page.getByRole('checkbox', { name: 'Critical' }).click();
		await page.getByLabelText('Rules').fill('security.*, system.ssh.*');
		await page.getByLabelText('Rule labels').fill('category=security\nbroken');
		await page.getByLabelText('Repeat after minutes (0 = never)').fill('240');
		await page.getByRole('checkbox', { name: 'Escalate when nobody acknowledges' }).click();
		await page
			.getByRole('group', { name: 'Also to' })
			.getByRole('checkbox', { name: 'On call' })
			.click();
		await page.getByRole('button', { name: 'Create policy' }).click();
		await expect
			.element(page.getByRole('alert'))
			.toHaveTextContent('Line 2 of the labels needs the form key=value.');

		await page.getByLabelText('Rule labels').fill('category=security');
		// Tags of the node (M2-29): a bad pair is named, nothing is sent.
		await page.getByLabelText('Node tags').fill('env=prod Role=db');
		await page.getByRole('button', { name: 'Create policy' }).click();
		await expect
			.element(page.getByRole('alert'))
			.toHaveTextContent('"Role=db" is not a valid tag (key=value).');
		expect(createPolicy).not.toHaveBeenCalled();
		await page.getByLabelText('Node tags').fill('env=prod');
		await page.getByRole('button', { name: 'Create policy' }).click();
		await vi.waitFor(() => expect(createPolicy).toHaveBeenCalledOnce());
		expect(createPolicy.mock.calls[0][0]).toEqual({
			spec: {
				name: 'Security',
				enabled: true,
				channelIds: ['c1'],
				grouping: PolicyGrouping.NODE,
				repeatMinutes: 240,
				escalation: { afterMinutes: 15, channelIds: ['c2'] },
				matchers: {
					severities: [AlertSeverity.CRITICAL],
					groupIds: [],
					rules: ['security.*', 'system.ssh.*'],
					labels: { category: 'security' },
					tags: { env: 'prod' }
				}
			}
		});
	});
});
