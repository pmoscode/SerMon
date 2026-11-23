import { create } from '@bufbuild/protobuf';
import { timestampFromMs } from '@bufbuild/protobuf/wkt';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { adminApi } from '#lib/api/admin.js';
import { Code } from '#lib/api/client.js';
import {
	GetLogSettingsResponseSchema,
	LogSeverity,
	TestRedactionResponseSchema
} from '#lib/api/gen/sermon/web/v1/log_settings_pb.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { stepUpState } from '#lib/stores/stepup.svelte.js';
import { rpcError, stepUpError } from '#lib/test/fakes.js';
import { ConnectError } from '@connectrpc/connect';
import LogSettingsPanel from './LogSettingsPanel.svelte';

const web = '0193f5a0-0000-7000-8000-0000000000aa';
const db = '0193f5a0-0000-7000-8000-0000000000ab';
const hostile = '<img src=x onerror=alert(1)> rule 1: look-around \u202Enot supported';

function settings(viewersSeeLogs = true) {
	return create(GetLogSettingsResponseSchema, {
		version: 5n,
		viewersSeeLogs,
		groups: [
			{
				groupId: web,
				groupName: 'web',
				custom: true,
				settings: {
					enabled: true,
					minSeverity: LogSeverity.WARNING,
					excludeUnits: ['cron.service'],
					files: [{ pattern: '/var/log/nginx/*.log', multilineStart: '' }]
				}
			},
			{ groupId: db, groupName: 'db', settings: { enabled: false } }
		],
		redactionRules: [{ pattern: 'customer-\\d{6}' }],
		nodes: [
			{
				nodeId: 'n1',
				hostname: 'web-01',
				groupId: web,
				appliedVersion: 5n,
				reportedAt: timestampFromMs(Date.now() - 60_000)
			},
			{ nodeId: 'n2', hostname: 'web-02', groupId: web, appliedVersion: 4n, logsVetoed: true },
			{ nodeId: 'n3', hostname: 'db-01', groupId: db, appliedVersion: 3n, error: hostile },
			{ nodeId: 'n4', hostname: 'db-02', groupId: db }
		]
	});
}

describe('LogSettingsPanel', () => {
	beforeEach(() => switchLocale('en'));
	afterEach(() => {
		vi.restoreAllMocks();
		if (stepUpState.open) stepUpState.finish(false);
	});

	it('shows the switch, the groups and the state per node, host texts as text', async () => {
		vi.spyOn(adminApi.logSettings, 'getLogSettings').mockResolvedValue(settings());
		render(LogSettingsPanel);
		await expect.element(page.getByText('Viewers see logs and security events.')).toBeVisible();
		await expect
			.element(page.getByText('Warning and above · 1 units excluded · 1 file patterns'))
			.toBeVisible();
		await expect.element(page.getByText('Logs off')).toBeVisible();
		const table = page.getByRole('table');
		await expect.element(table).toMatchTextContent(/web-01.*Current \(version 5\)/);
		await expect.element(table).toMatchTextContent(/web-02.*Waiting for version 5 \(has 4\)/);
		await expect.element(table).toMatchTextContent(/allow_logs = false/);
		await expect.element(table).toMatchTextContent(/db-02.*No answer yet/);
		await expect.element(table).toMatchTextContent(/Refused, works with version 3/);
		// The agent's error is the host's text: shown as text, the bidi override visible.
		await expect.element(table.getByText('⟨U+202E⟩')).toBeVisible();
		expect(document.querySelector('img[src="x"]')).toBeNull();
	});

	it('switches viewers off after a step-up', async () => {
		const load = vi
			.spyOn(adminApi.logSettings, 'getLogSettings')
			.mockResolvedValueOnce(settings(true))
			.mockResolvedValueOnce(settings(false));
		const set = vi
			.spyOn(adminApi.logSettings, 'setViewerLogAccess')
			.mockRejectedValueOnce(stepUpError())
			.mockResolvedValueOnce({} as never);
		render(LogSettingsPanel);
		await page.getByRole('button', { name: 'Hide from viewers' }).click();
		await vi.waitFor(() => expect(stepUpState.open).toBe(true));
		stepUpState.finish(true);
		await expect.element(page.getByText('Viewers see no logs and security events.')).toBeVisible();
		expect(set).toHaveBeenCalledTimes(2);
		expect(set.mock.calls[1][0]).toEqual({ viewersSeeLogs: false });
		expect(load).toHaveBeenCalledTimes(2);
	});

	it('edits a group and refuses what the agent would not take', async () => {
		vi.spyOn(adminApi.logSettings, 'getLogSettings').mockResolvedValue(settings());
		const save = vi
			.spyOn(adminApi.logSettings, 'setGroupLogSettings')
			.mockResolvedValue({} as never);
		render(LogSettingsPanel);
		await page.getByRole('button', { name: 'Edit the settings of web' }).click();
		await page.getByRole('button', { name: 'Add a pattern' }).click();
		await page.getByLabelText('Pattern 2', { exact: true }).fill('/var/log/[ab].log');
		await page.getByRole('button', { name: 'Save', exact: true }).click();
		await expect.element(page.getByText(/Only \* and \? are wildcards/)).toBeVisible();
		expect(save).not.toHaveBeenCalled();

		await page.getByLabelText('Pattern 2', { exact: true }).fill('/srv/app/logs/*.log');
		await page.getByLabelText('Start of an entry in pattern 2').fill('^\\d{4}-');
		await page.getByLabelText('Excluded units').fill('cron.service\nsnapd.service');
		await page.getByLabelText('Minimum severity').selectOptions('Error and above');
		await page.getByRole('button', { name: 'Save', exact: true }).click();
		await vi.waitFor(() => expect(save).toHaveBeenCalled());
		expect(save.mock.calls[0][0]).toEqual({
			groupId: web,
			settings: {
				enabled: true,
				minSeverity: LogSeverity.ERROR,
				excludeUnits: ['cron.service', 'snapd.service'],
				files: [
					{ pattern: '/var/log/nginx/*.log', multilineStart: '' },
					{ pattern: '/srv/app/logs/*.log', multilineStart: '^\\d{4}-' }
				]
			}
		});
	});

	it("shows the server's reason for a refused rule and tests rules before saving", async () => {
		vi.spyOn(adminApi.logSettings, 'getLogSettings').mockResolvedValue(settings());
		vi.spyOn(adminApi.logSettings, 'setRedactionRules').mockRejectedValue(
			new ConnectError('rule 2: look-around is not supported', Code.InvalidArgument)
		);
		const test = vi
			.spyOn(adminApi.logSettings, 'testRedaction')
			.mockResolvedValue(create(TestRedactionResponseSchema, { redacted: 'pin *** for <b>x</b>' }));
		render(LogSettingsPanel);
		await page.getByRole('button', { name: 'Add a rule' }).click();
		await page.getByLabelText('Rule 2').fill('(?<=pin )\\d+');
		await page.getByLabelText('Sample line to test').fill('pin 1234 for <b>x</b>');
		await page.getByRole('button', { name: 'Test', exact: true }).click();
		await expect
			.element(page.getByTestId('redaction-result'))
			.toHaveTextContent('pin *** for <b>x</b>');
		expect(test.mock.calls[0][0]).toEqual({
			rules: [{ pattern: 'customer-\\d{6}' }, { pattern: '(?<=pin )\\d+' }],
			sample: 'pin 1234 for <b>x</b>'
		});
		expect(document.querySelector('[data-testid="redaction-result"] b')).toBeNull();
		await page.getByRole('button', { name: 'Save the rules' }).click();
		await expect
			.element(page.getByRole('alert'))
			.toHaveTextContent('Not saved: rule 2: look-around is not supported');
	});

	it('says so without the right to manage logs', async () => {
		vi.spyOn(adminApi.logSettings, 'getLogSettings').mockRejectedValue(
			rpcError(Code.PermissionDenied)
		);
		render(LogSettingsPanel);
		await expect.element(page.getByRole('alert')).toBeVisible();
		await expect.element(page.getByRole('button', { name: 'Try again' })).toBeVisible();
	});
});
