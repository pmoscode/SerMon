import { create, type MessageInitShape } from '@bufbuild/protobuf';
import { timestampFromMs } from '@bufbuild/protobuf/wkt';
import { ConnectError } from '@connectrpc/connect';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { api, Code } from '#lib/api/client.js';
import { modulesApi } from '#lib/api/modules.js';
import { ListMetricsResponseSchema } from '#lib/api/gen/sermon/web/v1/metrics_pb.js';
import {
	GetModuleSettingsResponseSchema,
	GetNodeModulesResponseSchema,
	ListModuleHintsResponseSchema,
	ListModulesResponseSchema,
	ModuleState
} from '#lib/api/gen/sermon/web/v1/module_pb.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { sessionState } from '#lib/stores/session.svelte.js';
import { stepUpState } from '#lib/stores/stepup.svelte.js';
import { fakeSession, stepUpError } from '#lib/test/fakes.js';
import FleetModuleHints from './FleetModuleHints.svelte';
import ModuleSettingsPanel from './ModuleSettingsPanel.svelte';
import NodeModules from './NodeModules.svelte';

const web = '0193f5a0-0000-7000-8000-0000000000aa';
const db = '0193f5a0-0000-7000-8000-0000000000ab';
const node = '0193f5a0-0000-7000-8000-000000000001';
const hostile = '<img src=x onerror=alert(1)> refused \u202Eby host';
const simulateSchema = JSON.stringify({
	type: 'object',
	additionalProperties: false,
	properties: {
		behavior: { type: 'string', enum: ['ok', 'fail'], maxLength: 8, default: 'ok' },
		value: { type: 'integer', minimum: 0, maximum: 1000, default: 1 },
		token: { type: 'string', writeOnly: true, maxLength: 64 }
	}
});

function catalog() {
	vi.spyOn(modulesApi.modules, 'listModules').mockResolvedValue(
		create(ListModulesResponseSchema, {
			modules: [
				{ id: 'simulate', schemaJson: simulateSchema, secretFields: ['token'] },
				{
					id: 'smart',
					schemaJson: '{"type":"object","additionalProperties":false,"properties":{}}'
				}
			]
		})
	);
}

function settings() {
	return create(GetModuleSettingsResponseSchema, {
		version: 4n,
		groups: [
			{
				groupId: web,
				groupName: 'web',
				available: ['simulate', 'unknown_one'],
				modules: [
					{
						moduleId: 'simulate',
						enabled: true,
						interval: { seconds: 20n },
						configJson: '{"value":3}',
						secretsSet: ['token']
					}
				]
			},
			{ groupId: db, groupName: 'db', available: [] }
		]
	});
}

function asAdmin(admin: boolean) {
	sessionState.current = { ...fakeSession('Org'), orgAdmin: admin };
}

describe('ModuleSettingsPanel', () => {
	beforeEach(() => switchLocale('en'));
	afterEach(() => {
		vi.restoreAllMocks();
		if (stepUpState.open) stepUpState.finish(false);
	});

	it('lists per group the modules its agents have', async () => {
		catalog();
		vi.spyOn(modulesApi.modules, 'getModuleSettings').mockResolvedValue(settings());
		render(ModuleSettingsPanel);
		await expect.element(page.getByRole('heading', { name: 'web' })).toBeVisible();
		await expect.element(page.getByText('Test module (simulate)')).toBeVisible();
		await expect.element(page.getByText('On · every 20 s')).toBeVisible();
		await expect
			.element(page.getByText("The nodes of this group haven't reported any modules yet."))
			.toBeVisible();
		// A module the server doesn't know is not offered.
		expect(document.body.textContent).not.toContain('unknown_one');
	});

	it('saves a module after the step-up and shows what the server refuses', async () => {
		catalog();
		vi.spyOn(modulesApi.modules, 'getModuleSettings').mockResolvedValue(settings());
		const set = vi
			.spyOn(modulesApi.modules, 'setGroupModule')
			.mockRejectedValueOnce(stepUpError())
			.mockRejectedValueOnce(
				new ConnectError('simulate: value: 1001 is not within 0 to 1000', Code.InvalidArgument)
			)
			.mockResolvedValueOnce({} as never);
		render(ModuleSettingsPanel);
		await page.getByRole('button', { name: 'Set up: Test module (simulate) (web)' }).click();
		await expect.element(page.getByLabelText('Value')).toHaveValue('3');
		// The stored credential is named, its value never shown.
		expect(page.getByTestId('secret-state').element().textContent?.trim()).toBe('Set');
		await expect.element(page.getByLabelText(/^Token/)).toHaveValue('');
		await page.getByLabelText('Value').fill('7');
		await page.getByRole('button', { name: 'Save' }).click();
		await vi.waitFor(() => expect(stepUpState.open).toBe(true));
		stepUpState.finish(true);
		await expect
			.element(page.getByText('Refused: simulate: value: 1001 is not within 0 to 1000'))
			.toBeVisible();
		await page.getByRole('button', { name: 'Save' }).click();
		await vi.waitFor(() => expect(set).toHaveBeenCalledTimes(3));
		expect(set.mock.calls[2][0]).toEqual({
			groupId: web,
			setting: {
				moduleId: 'simulate',
				enabled: true,
				configJson: '{"value":7}',
				interval: { seconds: 20n }
			},
			secrets: {},
			clearSecrets: []
		});
	});

	it('opens the module a suggestion names, with the agent suggestion', async () => {
		catalog();
		vi.spyOn(modulesApi.modules, 'getModuleSettings').mockResolvedValue(settings());
		render(ModuleSettingsPanel, { opening: { group: db, module: 'smart', suggestion: '{}' } });
		// db has no node with smart: not offered, nothing opens.
		await expect.element(page.getByRole('heading', { name: 'db' })).toBeVisible();
		expect(page.getByRole('button', { name: 'Save' }).elements()).toHaveLength(0);
	});

	it('removes a module', async () => {
		catalog();
		vi.spyOn(modulesApi.modules, 'getModuleSettings').mockResolvedValue(settings());
		const remove = vi.spyOn(modulesApi.modules, 'removeGroupModule').mockResolvedValue({} as never);
		render(ModuleSettingsPanel);
		await page.getByRole('button', { name: 'Remove: Test module (simulate) (web)' }).click();
		await vi.waitFor(() =>
			expect(remove).toHaveBeenCalledWith({ groupId: web, moduleId: 'simulate' })
		);
	});
});

function nodeModules(over: MessageInitShape<typeof GetNodeModulesResponseSchema> = {}) {
	return create(GetNodeModulesResponseSchema, {
		groupId: web,
		builtin: ['simulate', 'smart'],
		settings: [
			{ moduleId: 'simulate', enabled: true },
			{ moduleId: 'smart', enabled: false }
		],
		health: [
			{
				moduleId: 'simulate',
				state: ModuleState.ERROR,
				reason: hostile,
				lastSuccess: timestampFromMs(Date.now() - 120_000)
			}
		],
		hints: [
			{
				moduleId: 'smart',
				reasons: ['process:smartd'],
				suggestedConfigJson: '{"exclude":["sdb"]}'
			},
			{ moduleId: 'simulate', reasons: ['process:simulate'] }
		],
		...over
	});
}

describe('NodeModules', () => {
	beforeEach(() => {
		switchLocale('en');
		vi.spyOn(api.metrics, 'listMetrics').mockResolvedValue(create(ListMetricsResponseSchema, {}));
	});
	afterEach(() => vi.restoreAllMocks());

	it('shows the state of each enabled module, the reason as text', async () => {
		asAdmin(false);
		vi.spyOn(modulesApi.modules, 'getNodeModules').mockResolvedValue(nodeModules());
		render(NodeModules, { nodeId: node, now: new Date() });
		await expect
			.element(page.getByRole('heading', { name: 'Test module (simulate)' }))
			.toBeVisible();
		await expect.element(page.getByTestId('module-state')).toHaveTextContent('Error');
		await expect.element(page.getByText('Last success 2 minutes ago')).toBeVisible();
		await expect.element(page.getByText(/⟨U\+202E⟩/)).toBeVisible();
		expect(document.querySelector('img[src="x"]')).toBeNull();
		// smart is set up but off: no section, but a suggestion.
		expect(page.getByRole('heading', { name: 'SMART' }).elements()).toHaveLength(0);
	});

	it('suggests modules the group does not run – admins can enable them', async () => {
		asAdmin(true);
		vi.spyOn(modulesApi.modules, 'getNodeModules').mockResolvedValue(nodeModules());
		render(NodeModules, { nodeId: node, now: new Date() });
		const hint = page.getByTestId('module-hint');
		await expect.element(hint).toBeVisible();
		expect(hint.element().textContent).toContain('SMART detected – enable the module?');
		expect(hint.elements()).toHaveLength(1);
		await expect.element(hint.getByText('Seen: process:smartd')).toBeVisible();
		const link = hint.getByRole('link', { name: 'Enable' }).element() as HTMLAnchorElement;
		const q = new URL(link.href).searchParams;
		expect([q.get('tab'), q.get('group'), q.get('module'), q.get('suggest')]).toEqual([
			'modules',
			web,
			'smart',
			'{"exclude":["sdb"]}'
		]);
	});

	it('asks viewers to see an admin, and says when the host forbids modules', async () => {
		asAdmin(false);
		vi.spyOn(modulesApi.modules, 'getNodeModules').mockResolvedValue(nodeModules({ vetoed: true }));
		render(NodeModules, { nodeId: node, now: new Date() });
		await expect.element(page.getByText('An admin can turn it on for the group.')).toBeVisible();
		await expect.element(page.getByText(/allow_modules = false/)).toBeVisible();
		expect(page.getByRole('link', { name: 'Enable' }).elements()).toHaveLength(0);
	});

	it('says when nothing runs', async () => {
		asAdmin(false);
		vi.spyOn(modulesApi.modules, 'getNodeModules').mockResolvedValue(
			nodeModules({ builtin: [], settings: [], health: [], hints: [] })
		);
		render(NodeModules, { nodeId: node, now: new Date() });
		await expect.element(page.getByText('The group of this node runs no module.')).toBeVisible();
		await expect.element(page.getByText(/hasn't reported yet which modules/)).toBeVisible();
	});
});

describe('FleetModuleHints', () => {
	beforeEach(() => switchLocale('en'));
	afterEach(() => vi.restoreAllMocks());

	it('lists the suggestions of the fleet, host names as text', async () => {
		asAdmin(true);
		vi.spyOn(modulesApi.modules, 'listModuleHints').mockResolvedValue(
			create(ListModuleHintsResponseSchema, {
				hints: [
					{
						nodeId: node,
						hostname: 'web-01\u202E',
						groupId: web,
						hint: { moduleId: 'smart', reasons: ['process:smartd'] }
					}
				]
			})
		);
		render(FleetModuleHints);
		await expect.element(page.getByTestId('fleet-module-hint')).toBeVisible();
		expect(page.getByTestId('fleet-module-hint').element().textContent).toContain(
			'SMART detected on web-01⟨U+202E⟩ – enable the module?'
		);
		await expect.element(page.getByRole('link', { name: 'Enable' })).toBeVisible();
	});

	it('shows nothing without suggestions or when they fail', async () => {
		vi.spyOn(modulesApi.modules, 'listModuleHints').mockRejectedValue(new Error('down'));
		render(FleetModuleHints);
		await new Promise((r) => setTimeout(r, 50));
		expect(page.getByTestId('fleet-module-hint').elements()).toHaveLength(0);
	});
});
