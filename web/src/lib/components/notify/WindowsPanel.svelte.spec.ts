import { create } from '@bufbuild/protobuf';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { api } from '#lib/api/client.js';
import {
	ListMaintenanceWindowsResponseSchema,
	Weekday
} from '#lib/api/gen/sermon/web/v1/silence_pb.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import WindowsPanel from './WindowsPanel.svelte';

describe('WindowsPanel', () => {
	beforeEach(() => {
		switchLocale('en');
		vi.spyOn(api.access, 'listNodeGroups').mockResolvedValue({
			groups: [{ id: 'g1', name: 'web' }]
		} as never);
	});
	afterEach(() => vi.restoreAllMocks());

	it('creates a weekly window and lists it in words', async () => {
		vi.spyOn(api.windows, 'listMaintenanceWindows')
			.mockResolvedValueOnce(create(ListMaintenanceWindowsResponseSchema, {}))
			.mockResolvedValue(
				create(ListMaintenanceWindowsResponseSchema, {
					windows: [
						{
							id: 'w1',
							spec: {
								name: 'Patches',
								enabled: true,
								groupIds: ['g1'],
								days: [Weekday.SUNDAY],
								startMinute: 180,
								durationMinutes: 120,
								timeZone: 'Europe/Berlin'
							}
						}
					]
				})
			);
		const createWindow = vi
			.spyOn(api.windows, 'createMaintenanceWindow')
			.mockResolvedValue({} as never);
		render(WindowsPanel);

		await page.getByRole('button', { name: 'New maintenance window' }).click();
		await page.getByLabelText('Name').fill('Patches');
		await page.getByRole('checkbox', { name: 'Sunday' }).click();
		await page.getByRole('checkbox', { name: 'web' }).click();
		await page.getByLabelText('Time zone').selectOptions('Europe/Berlin');
		await page.getByRole('button', { name: 'Create maintenance window' }).click();

		await vi.waitFor(() => expect(createWindow).toHaveBeenCalledOnce());
		expect(createWindow.mock.calls[0][0]).toEqual({
			spec: {
				name: 'Patches',
				enabled: true,
				groupIds: ['g1'],
				days: [Weekday.SUNDAY],
				startMinute: 180,
				durationMinutes: 120,
				timeZone: 'Europe/Berlin'
			}
		});
		await expect
			.element(page.getByText('Sunday from 03:00 for 2 h (Europe/Berlin) · web'))
			.toBeVisible();
	});
});
