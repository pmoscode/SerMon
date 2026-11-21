import { create } from '@bufbuild/protobuf';
import { timestampFromMs } from '@bufbuild/protobuf/wkt';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { api } from '#lib/api/client.js';
import { alertingApi } from '#lib/api/alerting.js';
import { GetFleetSummaryResponseSchema } from '#lib/api/gen/sermon/web/v1/fleet_pb.js';
import { ListSilencesResponseSchema } from '#lib/api/gen/sermon/web/v1/silence_pb.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { sessionState } from '#lib/stores/session.svelte.js';
import { stepUpState } from '#lib/stores/stepup.svelte.js';
import { fakeSession, stepUpError } from '#lib/test/fakes.js';
import SilencesPanel from './SilencesPanel.svelte';

const fleet = create(GetFleetSummaryResponseSchema, {
	nodes: [
		{ nodeId: 'n1', hostname: 'web-01', groupId: 'g1', groupName: 'web' },
		{ nodeId: 'n2', hostname: 'db-01', groupId: 'g2', groupName: 'db' }
	]
});
const hostile = '<b>reboot</b> @everyone';

function session(groups: string[]) {
	sessionState.current = { ...fakeSession(), silenceGroupIds: groups };
	sessionState.status = 'authenticated';
}

describe('SilencesPanel', () => {
	beforeEach(() => {
		switchLocale('en');
		vi.spyOn(api.fleet, 'getFleetSummary').mockResolvedValue(fleet);
	});
	afterEach(() => {
		vi.restoreAllMocks();
		if (stepUpState.open) stepUpState.finish(false);
		sessionState.current = undefined;
		sessionState.status = 'unknown';
	});

	it('creates a silence prefilled from an alert, after the step-up', async () => {
		session(['g1']);
		vi.spyOn(alertingApi.silences, 'listSilences').mockResolvedValue(
			create(ListSilencesResponseSchema, {})
		);
		const createSilence = vi
			.spyOn(alertingApi.silences, 'createSilence')
			.mockRejectedValueOnce(stepUpError())
			.mockResolvedValueOnce({} as never);
		render(SilencesPanel, {
			draft: { groupIds: ['g1'], nodeIds: ['n1'], rules: ['system.cpu.usage_high'] }
		});

		// Only the groups the operator may mute.
		await expect.element(page.getByRole('checkbox', { name: 'web', exact: true })).toBeChecked();
		expect(page.getByRole('checkbox', { name: 'db', exact: true }).elements()).toHaveLength(0);
		await expect.element(page.getByRole('checkbox', { name: 'web-01' })).toBeChecked();
		await expect.element(page.getByLabelText('Rules')).toHaveValue('system.cpu.usage_high');
		await page.getByLabelText('Comment (why?)').fill('Kernel update');
		await page.getByRole('button', { name: 'Mute' }).click();
		await vi.waitFor(() => expect(stepUpState.open).toBe(true));
		stepUpState.finish(true);

		await vi.waitFor(() => expect(createSilence).toHaveBeenCalledTimes(2));
		const spec = createSilence.mock.calls[1][0].spec!;
		expect(spec.matchers).toEqual({
			severities: [],
			groupIds: ['g1'],
			rules: ['system.cpu.usage_high'],
			labels: {}
		});
		expect(spec.nodeIds).toEqual(['n1']);
		expect(spec.comment).toBe('Kernel update');
		const minutes = (Number(spec.endTime!.seconds) * 1000 - Date.now()) / 60_000;
		expect(minutes).toBeGreaterThan(235);
		expect(minutes).toBeLessThan(241);
	});

	it('lists silences with the comment as text and ends them; viewers only read', async () => {
		session(['g1']);
		const now = Date.now();
		vi.spyOn(alertingApi.silences, 'listSilences').mockResolvedValue(
			create(ListSilencesResponseSchema, {
				silences: [
					{
						id: 's1',
						spec: {
							matchers: { groupIds: ['g1'], rules: ['security.*'] },
							nodeIds: ['n1'],
							startTime: timestampFromMs(now - 60_000),
							endTime: timestampFromMs(now + 3_600_000),
							comment: hostile
						}
					},
					{
						id: 's2',
						spec: {
							matchers: { groupIds: ['g2'] },
							startTime: timestampFromMs(now - 60_000),
							endTime: timestampFromMs(now + 3_600_000),
							comment: 'db work'
						}
					}
				]
			})
		);
		const expire = vi.spyOn(alertingApi.silences, 'expireSilence').mockResolvedValue({} as never);
		const view = await render(SilencesPanel);

		await expect.element(page.getByText(hostile)).toBeVisible();
		expect(document.querySelector('ul b')).toBeNull();
		await expect
			.element(page.getByText('Groups: web · Nodes: web-01 · Rules: security.*'))
			.toBeVisible();
		// Only the silence of the operator's group can be ended here.
		expect(page.getByRole('button', { name: 'End now' }).elements()).toHaveLength(1);
		await page.getByRole('button', { name: 'End now' }).click();
		await vi.waitFor(() => expect(expire).toHaveBeenCalledWith({ silenceId: 's1' }));
		await view.unmount();

		session([]);
		render(SilencesPanel);
		await expect.element(page.getByText('db work')).toBeVisible();
		expect(page.getByRole('button', { name: 'New silence' }).elements()).toHaveLength(0);
		expect(page.getByRole('button', { name: 'End now' }).elements()).toHaveLength(0);
	});
});
