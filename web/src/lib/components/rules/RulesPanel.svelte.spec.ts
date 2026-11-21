import { create } from '@bufbuild/protobuf';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { alertingApi } from '#lib/api/alerting.js';
import {
	AlertRuleChangeAction,
	AlertRuleOrigin,
	ImportAlertRulesResponseSchema,
	ListAlertRulesResponseSchema
} from '#lib/api/gen/sermon/web/v1/alert_rule_service_pb.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { sessionState } from '#lib/stores/session.svelte.js';
import { stepUpState } from '#lib/stores/stepup.svelte.js';
import { fakeSession, stepUpError } from '#lib/test/fakes.js';
import RulesPanel from './RulesPanel.svelte';

const cpuYaml = 'metadata:\n  id: system.cpu.usage_high\nspec:\n  window: 10m\n';
const rules = create(ListAlertRulesResponseSchema, {
	rules: [
		{
			id: 'system.cpu.usage_high',
			name: { de: 'Hohe CPU-Auslastung', en: 'High CPU usage' },
			type: 'threshold',
			origin: AlertRuleOrigin.CHANGED,
			enabled: true,
			yaml: cpuYaml.replace('10m', '15m'),
			defaultYaml: cpuYaml,
			help: { de: 'Hilfe', en: 'The CPU was <b>busy</b>.' }
		},
		{
			id: 'system.swap.usage_high',
			name: { en: 'High swap usage' },
			type: 'threshold',
			origin: AlertRuleOrigin.DEFAULT,
			enabled: false,
			yaml: 'x'
		},
		{
			id: 'custom.web.slow',
			name: { en: 'Slow web' },
			type: 'rate',
			origin: AlertRuleOrigin.OWN,
			enabled: true,
			yaml: 'y',
			problem: 'unknown metric "foo"'
		}
	]
});

function session(admin: boolean) {
	sessionState.current = { ...fakeSession(), orgAdmin: admin };
	sessionState.status = 'authenticated';
}

describe('RulesPanel', () => {
	beforeEach(() => {
		switchLocale('en');
		vi.spyOn(alertingApi.rules, 'listAlertRules').mockResolvedValue(rules);
	});
	afterEach(() => {
		vi.restoreAllMocks();
		if (stepUpState.open) stepUpState.finish(false);
		sessionState.current = undefined;
		sessionState.status = 'unknown';
	});

	it('shows rules with origin, state, help as text and the diff to the default', async () => {
		session(false);
		render(RulesPanel);
		await expect.element(page.getByText('High CPU usage')).toBeVisible();
		await expect.element(page.getByText('Changed')).toBeVisible();
		await expect.element(page.getByText('off', { exact: true })).toBeVisible();
		await expect.element(page.getByRole('alert')).toMatchTextContent('unknown metric "foo"');
		// Viewers read, but have no actions.
		expect(page.getByRole('button', { name: 'Switch off' }).elements()).toHaveLength(0);
		expect(page.getByRole('button', { name: 'Import' }).elements()).toHaveLength(0);

		await page.getByRole('button', { name: 'Details' }).first().click();
		await expect.element(page.getByText('The CPU was <b>busy</b>.')).toBeVisible();
		const diff = page.getByRole('figure', {
			name: 'Difference to the default rule system.cpu.usage_high'
		});
		await expect.element(diff).toMatchTextContent(/- removed: window: 10m/);
		await expect.element(diff).toMatchTextContent(/\+ added: window: 15m/);
	});

	it('checks in a dry run, shows problems at their place and saves after the step-up', async () => {
		session(true);
		const imp = vi
			.spyOn(alertingApi.rules, 'importAlertRules')
			.mockResolvedValueOnce(
				create(ImportAlertRulesResponseSchema, {
					problems: [{ document: 2, line: 14, message: 'line 17: unknown field "query"' }]
				})
			)
			.mockResolvedValueOnce(
				create(ImportAlertRulesResponseSchema, {
					changes: [
						{
							ruleId: 'system.cpu.usage_high',
							action: AlertRuleChangeAction.UPDATE,
							beforeYaml: 'a: 1\n',
							afterYaml: 'a: 2\n'
						}
					]
				})
			)
			.mockRejectedValueOnce(stepUpError())
			.mockResolvedValueOnce(create(ImportAlertRulesResponseSchema, { applied: true }));
		render(RulesPanel);

		await page.getByRole('button', { name: 'Import' }).click();
		await page.getByLabelText(/Rules as YAML/).fill('a: 2\n');
		await page.getByRole('button', { name: 'Check' }).click();
		await expect
			.element(page.getByRole('region', { name: 'Import' }).getByRole('alert'))
			.toMatchTextContent('Document 2 (from line 14): line 17: unknown field "query"');
		await page.getByRole('button', { name: 'Check' }).click();
		await expect
			.element(page.getByRole('figure', { name: 'Changes to system.cpu.usage_high' }))
			.toMatchTextContent(/\+ added: a: 2/);
		expect(imp.mock.calls.map((c) => c[0].dryRun)).toEqual([true, true]);

		await page.getByRole('button', { name: 'Save' }).click();
		await vi.waitFor(() => expect(stepUpState.open).toBe(true));
		stepUpState.finish(true);
		await vi.waitFor(() => expect(imp).toHaveBeenCalledTimes(4));
		expect(imp.mock.calls[3][0]).toEqual({ yaml: 'a: 2\n', dryRun: false });
		// Saved: back to the list.
		await expect.element(page.getByRole('button', { name: 'Import' })).toBeVisible();
	});

	it('switches, resets after a second click and exports', async () => {
		session(true);
		const enable = vi
			.spyOn(alertingApi.rules, 'setAlertRuleEnabled')
			.mockResolvedValue({} as never);
		const reset = vi.spyOn(alertingApi.rules, 'resetAlertRule').mockResolvedValue({} as never);
		const exp = vi
			.spyOn(alertingApi.rules, 'exportAlertRules')
			.mockResolvedValue({ yaml: '# export\n' } as never);
		const blob = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:x');
		vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {});
		vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
		render(RulesPanel);

		await page.getByRole('button', { name: 'Switch on' }).click();
		await vi.waitFor(() =>
			expect(enable).toHaveBeenCalledWith({ ruleId: 'system.swap.usage_high', enabled: true })
		);
		await page.getByRole('button', { name: 'Reset' }).click();
		expect(reset).not.toHaveBeenCalled();
		await page.getByRole('button', { name: 'Really delete' }).click();
		await vi.waitFor(() => expect(reset).toHaveBeenCalledWith({ ruleId: 'system.cpu.usage_high' }));
		await page.getByRole('button', { name: 'Export all' }).click();
		await vi.waitFor(() => expect(exp).toHaveBeenCalledWith({ all: true }));
		expect(blob).toHaveBeenCalledOnce();
	});
});
