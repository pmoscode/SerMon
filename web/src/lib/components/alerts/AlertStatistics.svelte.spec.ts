import { create } from '@bufbuild/protobuf';
import { durationFromMs } from '@bufbuild/protobuf/wkt';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { Code } from '#lib/api/client.js';
import { alertingApi } from '#lib/api/alerting.js';
import { GetAlertStatisticsResponseSchema } from '#lib/api/gen/sermon/web/v1/alert_pb.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { rpcError } from '#lib/test/fakes.js';
import AlertStatistics from './AlertStatistics.svelte';

const stats = create(GetAlertStatisticsResponseSchema, {
	totals: {
		firings: 12,
		acknowledged: 4,
		resolved: 10,
		open: 2,
		unacknowledged: 8,
		mtta: durationFromMs(5 * 60_000),
		mttr: durationFromMs(90 * 60_000)
	},
	rules: [
		{
			ruleId: 'system.cpu.usage_high',
			ruleName: { de: 'Hohe CPU-Auslastung', en: 'High CPU usage' },
			figures: { firings: 9, open: 1, flapping: 6, mttr: durationFromMs(120_000) },
			nodes: 3
		},
		{ ruleId: 'acme.own.rule', figures: { firings: 3 }, nodes: 1 }
	]
});

describe('AlertStatistics', () => {
	beforeEach(() => switchLocale('en'));
	afterEach(() => vi.restoreAllMocks());

	it('shows the totals and the loudest rules of 7 days, and 30 on request', async () => {
		const get = vi.spyOn(alertingApi.alerts, 'getAlertStatistics').mockResolvedValue(stats);
		render(AlertStatistics);
		await expect.element(page.getByText('12', { exact: true })).toBeVisible();
		await expect.element(page.getByText('5 min', { exact: true })).toBeVisible();
		await expect.element(page.getByText('1 hr 30 min', { exact: true })).toBeVisible();
		const rows = page.getByRole('row');
		await expect
			.element(rows.nth(1))
			.toMatchTextContent(/High CPU usage.*system\.cpu\.usage_high.*9.*3.*–.*2 min.*1.*6/);
		await expect.element(rows.nth(2)).toMatchTextContent(/acme\.own\.rule/);
		expect(get).toHaveBeenLastCalledWith({ days: 7 });
		await page.getByRole('button', { name: '30 days' }).click();
		await vi.waitFor(() => expect(get).toHaveBeenLastCalledWith({ days: 30 }));
	});

	it('says so for an empty range, a truncated history and a failure', async () => {
		const get = vi
			.spyOn(alertingApi.alerts, 'getAlertStatistics')
			.mockResolvedValueOnce(
				create(GetAlertStatisticsResponseSchema, { totals: {}, truncated: true })
			)
			.mockRejectedValueOnce(rpcError(Code.Unavailable));
		render(AlertStatistics);
		await expect.element(page.getByText('No alerts in this range.')).toBeVisible();
		await expect.element(page.getByText(/more history than one evaluation reads/)).toBeVisible();
		expect(page.getByText('–', { exact: true }).elements().length).toBeGreaterThanOrEqual(2);
		await page.getByRole('button', { name: '30 days' }).click();
		await expect.element(page.getByRole('alert')).toBeVisible();
		expect(get).toHaveBeenCalledTimes(2);
	});
});
