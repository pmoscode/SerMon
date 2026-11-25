import { create } from '@bufbuild/protobuf';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { Weekday } from '#lib/api/gen/sermon/web/v1/silence_pb.js';
import {
	ExportUpdatePolicyResponseSchema,
	ImportUpdatePolicyResponseSchema,
	SetUpdatePolicyResponseSchema,
	UpdateMode,
	UpdatePolicySchema
} from '#lib/api/gen/sermon/web/v1/update_policy_pb.js';
import { updatesApi } from '#lib/api/updates.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { stepUpState } from '#lib/stores/stepup.svelte.js';
import { stepUpError } from '#lib/test/fakes.js';
import PolicyEditor from './PolicyEditor.svelte';

const groupId = '0193f5a0-0000-7000-8000-0000000000c1';

function policy() {
	return create(UpdatePolicySchema, {
		groupId,
		groupName: 'web',
		isDefault: true,
		spec: { mode: UpdateMode.REPORT, maxParallel: 1, stopOnFailure: true, healthCheckMinutes: 10 }
	});
}

describe('PolicyEditor', () => {
	beforeEach(() => switchLocale('en'));
	afterEach(() => {
		vi.restoreAllMocks();
		if (stepUpState.open) stepUpState.finish(false);
	});

	it('needs a window for an automatic mode and saves the policy after the step-up', async () => {
		const set = vi
			.spyOn(updatesApi.policies, 'setUpdatePolicy')
			.mockRejectedValueOnce(stepUpError())
			.mockResolvedValueOnce(create(SetUpdatePolicyResponseSchema, {}));
		const saved = vi.fn();
		render(PolicyEditor, { policy: policy(), onsaved: saved });
		await page.getByRole('radio', { name: 'security updates automatically' }).click();
		await expect.element(page.getByText(/Alpine has no security updates/)).toBeVisible();
		await expect.element(page.getByText(/need a window/)).toBeVisible();
		await expect.element(page.getByRole('button', { name: 'Save' })).toBeDisabled();
		await page.getByRole('checkbox', { name: 'Sunday' }).click();
		await page.getByLabelText('Start (local time)').fill('03:30');
		await page.getByLabelText(/Time zone/).fill('Europe/Berlin');
		await page.getByLabelText(/At once/).fill('5');
		await page.getByRole('checkbox', { name: /Reboot after updates/ }).click();
		await page.getByRole('button', { name: 'Save' }).click();
		await vi.waitFor(() => expect(stepUpState.open).toBe(true));
		stepUpState.finish(true);
		await vi.waitFor(() => expect(set).toHaveBeenCalledTimes(2));
		expect(set.mock.calls[1][0]).toEqual({
			groupId,
			spec: {
				mode: UpdateMode.SECURITY_AUTO,
				autoReboot: true,
				maxParallel: 5,
				stopOnFailure: true,
				healthCheckMinutes: 10,
				window: {
					days: [Weekday.SUNDAY],
					startMinute: 210,
					durationMinutes: 120,
					timeZone: 'Europe/Berlin'
				}
			}
		});
		await vi.waitFor(() => expect(saved).toHaveBeenCalled());
	});

	it('exports and imports the YAML, resets to report only', async () => {
		vi.spyOn(updatesApi.policies, 'exportUpdatePolicy').mockResolvedValue(
			create(ExportUpdatePolicyResponseSchema, {
				yaml: 'kind: UpdatePolicy\nspec:\n  mode: report\n'
			})
		);
		const imported = vi
			.spyOn(updatesApi.policies, 'importUpdatePolicy')
			.mockResolvedValue(create(ImportUpdatePolicyResponseSchema, {}));
		const reset = vi.spyOn(updatesApi.policies, 'resetUpdatePolicy').mockResolvedValue({} as never);
		render(PolicyEditor, { policy: policy(), onsaved: () => {} });
		await page.getByRole('button', { name: 'YAML' }).click();
		const yaml = page.getByRole('textbox', { name: 'YAML' });
		await expect.element(yaml).toHaveValue('kind: UpdatePolicy\nspec:\n  mode: report\n');
		await yaml.fill('kind: UpdatePolicy\nspec:\n  mode: all-auto\n');
		await page.getByRole('button', { name: 'Apply YAML' }).click();
		await vi.waitFor(() =>
			expect(imported).toHaveBeenCalledWith({
				groupId,
				yaml: 'kind: UpdatePolicy\nspec:\n  mode: all-auto\n'
			})
		);
		await page.getByRole('button', { name: /Reset/ }).click();
		await vi.waitFor(() => expect(reset).toHaveBeenCalledWith({ groupId }));
	});
});
