import { create } from '@bufbuild/protobuf';
import { timestampFromMs } from '@bufbuild/protobuf/wkt';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { anomaliesApi } from '#lib/api/anomalies.js';
import { ListUnusualResponseSchema } from '#lib/api/gen/sermon/web/v1/anomaly_pb.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import UnusualPanel from './UnusualPanel.svelte';

const now = Date.now();
const range = { from: now - 3600_000, to: now };

function found() {
	return create(ListUnusualResponseSchema, {
		series: [
			{
				nodeId: 'n1',
				metric: 'sermon_cpu_utilization_ratio',
				labels: { mode: 'iowait' },
				maxScore: 12.34,
				anomalousMinutes: 9,
				peak: timestampFromMs(now - 600_000)
			}
		]
	});
}

describe('UnusualPanel', () => {
	beforeEach(() => switchLocale('en'));
	afterEach(() => vi.restoreAllMocks());

	it('checks the marked range of a node on request', async () => {
		const list = vi.spyOn(anomaliesApi, 'listUnusual').mockResolvedValue(found());
		render(UnusualPanel, { range, nodeId: 'n1' });
		await expect.element(page.getByText(/Mark a range in a chart/)).toBeVisible();
		expect(list).not.toHaveBeenCalled();
		await page.getByRole('button', { name: 'Check the range' }).click();
		await expect.element(page.getByRole('cell', { name: '12.3' })).toBeVisible();
		await expect.element(page.getByText('mode=iowait')).toBeVisible();
		await expect.element(page.getByRole('cell', { name: '9' })).toBeVisible();
		expect(list.mock.calls[0][0]).toMatchObject({ nodeId: 'n1', limit: 20 });
		// One node: no column of nodes.
		expect(page.getByRole('columnheader', { name: 'Node' }).elements()).toHaveLength(0);
	});

	it('lists the fleet at once with links to the nodes, or says there was nothing', async () => {
		const list = vi.spyOn(anomaliesApi, 'listUnusual').mockResolvedValueOnce(found());
		render(UnusualPanel, { range, hostnames: { n1: 'web-01' }, auto: true });
		await expect.element(page.getByRole('link', { name: 'web-01' })).toBeVisible();
		expect(list.mock.calls[0][0].nodeId).toBe('');

		list.mockResolvedValueOnce(create(ListUnusualResponseSchema, {}));
		render(UnusualPanel, { range: { from: now - 7200_000, to: now }, auto: true });
		await expect.element(page.getByText('Nothing unusual in this range.')).toBeVisible();
	});

	it('explains a range older than the results and a failure', async () => {
		const list = vi.spyOn(anomaliesApi, 'listUnusual').mockRejectedValue(new Error('down'));
		render(UnusualPanel, {
			range: { from: now - 40 * 86_400_000, to: now - 35 * 86_400_000 },
			auto: true
		});
		await expect.element(page.getByText('Anomalies exist for the last 30 days.')).toBeVisible();
		expect(list).not.toHaveBeenCalled();

		render(UnusualPanel, { range, nodeId: 'n1' });
		await page.getByRole('button', { name: 'Check the range' }).click();
		await expect.element(page.getByRole('alert')).toBeVisible();
	});
});
