import { create } from '@bufbuild/protobuf';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page, userEvent } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { adminApi } from '#lib/api/admin.js';
import {
	GetRetentionResponseSchema,
	SetRetentionResponseSchema
} from '#lib/api/gen/sermon/web/v1/access_pb.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { stepUpState } from '#lib/stores/stepup.svelte.js';
import { stepUpError } from '#lib/test/fakes.js';
import RetentionPanel from './RetentionPanel.svelte';

function current(over: { logs?: number; events?: number; cap?: number } = {}) {
	return create(GetRetentionResponseSchema, {
		logRetentionDays: over.logs ?? 0,
		eventRetentionDays: over.events ?? 90,
		logMinDays: 1,
		logMaxDays: 14,
		eventMinDays: 7,
		eventMaxDays: 365,
		capDays: over.cap ?? 0
	});
}

describe('RetentionPanel', () => {
	beforeEach(() => switchLocale('en'));
	afterEach(() => {
		vi.restoreAllMocks();
		if (stepUpState.open) stepUpState.finish(false);
	});

	it('shows the current retention, the limits and the cap', async () => {
		vi.spyOn(adminApi.access, 'getRetention').mockResolvedValue(current({ cap: 60 }));
		render(RetentionPanel);
		await expect.element(page.getByRole('spinbutton', { name: 'Logs (days)' })).toHaveValue(null);
		await expect
			.element(page.getByRole('spinbutton', { name: 'Security events and alert history (days)' }))
			.toHaveValue(90);
		await expect.element(page.getByText('1 to 14 days; empty: 14 days')).toBeVisible();
		await expect.element(page.getByText('Cap of this organization: 60 days')).toBeVisible();
	});

	it('refuses values out of range before asking the server', async () => {
		vi.spyOn(adminApi.access, 'getRetention').mockResolvedValue(current({ cap: 60 }));
		const set = vi.spyOn(adminApi.access, 'setRetention');
		render(RetentionPanel);
		const events = page.getByRole('spinbutton', {
			name: 'Security events and alert history (days)'
		});
		await expect.element(events).toHaveValue(90);
		await userEvent.fill(page.getByRole('spinbutton', { name: 'Logs (days)' }), '20');
		await expect.element(page.getByText(/^Logs: whole days/)).toBeVisible();
		await expect.element(page.getByText(/^Events: whole days/)).toBeVisible(); // 90 above the cap
		await expect.element(page.getByRole('button', { name: 'Save' })).toBeDisabled();
		expect(set).not.toHaveBeenCalled();
	});

	it('saves after the step-up', async () => {
		vi.spyOn(adminApi.access, 'getRetention').mockResolvedValue(current());
		const set = vi
			.spyOn(adminApi.access, 'setRetention')
			.mockRejectedValueOnce(stepUpError())
			.mockResolvedValueOnce(create(SetRetentionResponseSchema, {}));
		render(RetentionPanel);
		await userEvent.fill(page.getByRole('spinbutton', { name: 'Logs (days)' }), '3');
		await userEvent.fill(
			page.getByRole('spinbutton', { name: 'Security events and alert history (days)' }),
			''
		);
		await page.getByRole('button', { name: 'Save' }).click();
		await vi.waitFor(() => expect(stepUpState.open).toBe(true));
		stepUpState.finish(true);
		await vi.waitFor(() => expect(set).toHaveBeenCalledTimes(2));
		expect(set.mock.calls[1][0]).toEqual({ logRetentionDays: 3, eventRetentionDays: 0 });
		await expect.element(page.getByText(/^Saved\./)).toBeVisible();
	});
});
