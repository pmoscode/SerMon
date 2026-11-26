import { create } from '@bufbuild/protobuf';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { ModuleSettingSchema } from '#lib/api/gen/sermon/web/v1/module_pb.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import type { Saved } from '#lib/modules/schema.js';
import ModuleForm from './ModuleForm.svelte';

/** A value typed into a password field; for tests only. */
const TYPED = 'typed-in-value'; // betterleaks:allow

// Every kind of field of the subset (ADR 0039), titles as labels (a module of a newer agent).
const schema = JSON.stringify({
	type: 'object',
	additionalProperties: false,
	required: ['url'],
	properties: {
		url: {
			type: 'string',
			title: 'URL',
			format: 'uri',
			maxLength: 64,
			description: 'The status page.'
		},
		port: { type: 'integer', title: 'Port', minimum: 1, maximum: 65535 },
		ratio: { type: 'number', title: 'Ratio', minimum: 0, maximum: 1 },
		verify: { type: 'boolean', title: 'Verify', default: true },
		mode: { type: 'string', title: 'Mode', enum: ['plain', 'tls'], maxLength: 8 },
		password: { type: 'string', title: 'Password', writeOnly: true, maxLength: 64 },
		paths: {
			type: 'array',
			title: 'Paths',
			maxItems: 2,
			items: { type: 'string', format: 'path', maxLength: 64 }
		},
		codes: {
			type: 'array',
			title: 'Codes',
			maxItems: 3,
			items: { type: 'integer', minimum: 100, maximum: 599 }
		}
	}
});

function setup(props: Partial<Parameters<typeof render<typeof ModuleForm>>[1]> = {}) {
	const saved: Saved[] = [];
	const onSave = vi.fn(async (s: Saved) => {
		saved.push(s);
		return '';
	});
	render(ModuleForm, {
		moduleId: 'newer',
		schemaJson: schema,
		onSave,
		onCancel: () => {},
		...props
	});
	return { saved, onSave };
}

describe('ModuleForm', () => {
	beforeEach(() => switchLocale('en'));

	it('builds a control for every kind of field', async () => {
		setup();
		await expect.element(page.getByLabelText('URL *')).toBeVisible();
		await expect.element(page.getByText('The status page.')).toBeVisible();
		await expect.element(page.getByLabelText('Port')).toBeVisible();
		await expect.element(page.getByLabelText('Ratio')).toBeVisible();
		await expect.element(page.getByRole('checkbox', { name: 'Verify' })).toBeChecked();
		await expect.element(page.getByRole('combobox', { name: 'Mode' })).toBeVisible();
		const paths = page.getByLabelText('Paths');
		expect(paths.element().tagName).toBe('TEXTAREA');
		expect((page.getByLabelText(/^Password/).element() as HTMLInputElement).type).toBe('password');
		await expect.element(page.getByRole('checkbox', { name: 'Run the module' })).toBeChecked();
	});

	it("names what it can't send and doesn't save", async () => {
		const { onSave } = setup();
		await page.getByLabelText('Ratio').fill('x');
		await page.getByLabelText('Codes').fill('200\nseven');
		await page.getByLabelText('Interval in seconds').fill('5');
		await page.getByRole('button', { name: 'Save' }).click();
		await expect.element(page.getByText('Required.')).toBeVisible();
		await expect.element(page.getByText('A number.')).toBeVisible();
		await expect.element(page.getByText('Entry 2: A whole number.')).toBeVisible();
		await expect.element(page.getByText('10 to 3600 seconds.')).toBeVisible();
		expect(page.getByLabelText('Ratio').element().getAttribute('aria-invalid')).toBe('true');
		expect(page.getByLabelText('URL *').element().getAttribute('maxlength')).toBe('64');
		expect(onSave).not.toHaveBeenCalled();
	});

	it('saves the configuration in the types of the schema', async () => {
		const { saved } = setup();
		await page.getByLabelText('URL *').fill('http://127.0.0.1:8080/status');
		await page.getByLabelText('Port').fill('8080');
		await page.getByRole('checkbox', { name: 'Verify' }).click();
		await page.getByRole('combobox', { name: 'Mode' }).selectOptions('tls');
		await page.getByLabelText('Codes').fill('200\n301');
		await page.getByLabelText('Interval in seconds').fill('20');
		await page.getByRole('button', { name: 'Save' }).click();
		await vi.waitFor(() => expect(saved).toHaveLength(1));
		expect(saved[0]).toEqual({
			enabled: true,
			intervalSeconds: 20,
			configJson: JSON.stringify({
				url: 'http://127.0.0.1:8080/status',
				port: 8080,
				verify: false,
				mode: 'tls',
				codes: [200, 301]
			}),
			secrets: {},
			clearSecrets: []
		});
	});

	it('starts with the stored setting, or the suggestion of the agent', async () => {
		const setting = create(ModuleSettingSchema, {
			moduleId: 'newer',
			enabled: false,
			interval: { seconds: 30n },
			configJson: '{"url":"http://x/","paths":["/a","/b"]}'
		});
		setup({ setting, suggestion: '{"port":1}' });
		await expect.element(page.getByLabelText('URL *')).toHaveValue('http://x/');
		await expect.element(page.getByLabelText('Paths')).toHaveValue('/a\n/b');
		await expect.element(page.getByLabelText('Port')).toHaveValue('');
		await expect.element(page.getByLabelText('Interval in seconds')).toHaveValue('30');
		await expect.element(page.getByRole('checkbox', { name: 'Run the module' })).not.toBeChecked();
	});

	it('never shows a credential, only whether one is set', async () => {
		const { saved } = setup({
			secretsSet: ['password'],
			setting: create(ModuleSettingSchema, {
				moduleId: 'newer',
				enabled: true,
				configJson: '{"url":"http://x/"}'
			})
		});
		const field = page.getByLabelText(/^Password/).element() as HTMLInputElement;
		expect(field.value).toBe('');
		expect(field.type).toBe('password');
		expect(field.autocomplete).toBe('new-password');
		await expect.element(page.getByTestId('secret-state')).toHaveTextContent('Set');
		await page.getByLabelText(/^Password/).fill(TYPED);
		await page.getByRole('button', { name: 'Save' }).click();
		await vi.waitFor(() => expect(saved).toHaveLength(1));
		expect(saved[0].secrets).toEqual({ password: TYPED });
		expect(saved[0].configJson).not.toContain(TYPED);
	});

	it('removes a stored credential, and says a required one is then missing', async () => {
		const required = JSON.stringify({
			type: 'object',
			additionalProperties: false,
			required: ['password'],
			properties: {
				password: { type: 'string', title: 'Password', writeOnly: true, maxLength: 64 }
			}
		});
		const saved: Saved[] = [];
		render(ModuleForm, {
			moduleId: 'newer',
			schemaJson: required,
			secretsSet: ['password'],
			onSave: async (s: Saved) => {
				saved.push(s);
				return '';
			},
			onCancel: () => {}
		});
		await expect.element(page.getByText(/never shown again/)).toBeVisible();
		await page.getByRole('checkbox', { name: 'Remove the stored value' }).click();
		await page.getByRole('button', { name: 'Save' }).click();
		await expect.element(page.getByText('Required.')).toBeVisible();
		expect(saved).toHaveLength(0);
		// A new value instead is fine: it replaces the stored one.
		await page.getByLabelText(/^Password/).fill(TYPED);
		await page.getByRole('button', { name: 'Save' }).click();
		await vi.waitFor(() => expect(saved).toHaveLength(1));
		expect(saved[0]).toMatchObject({ secrets: { password: TYPED }, clearSecrets: [] });
	});

	it('removes an optional credential', async () => {
		const { saved } = setup({
			secretsSet: ['password'],
			setting: create(ModuleSettingSchema, {
				moduleId: 'newer',
				enabled: true,
				configJson: '{"url":"http://x/"}'
			})
		});
		await page.getByRole('checkbox', { name: 'Remove the stored value' }).click();
		await page.getByRole('button', { name: 'Save' }).click();
		await vi.waitFor(() => expect(saved).toHaveLength(1));
		expect(saved[0]).toMatchObject({ secrets: {}, clearSecrets: ['password'] });
	});

	it('labels the fields of a known module in the UI language', async () => {
		switchLocale('de');
		render(ModuleForm, {
			moduleId: 'simulate',
			schemaJson:
				'{"type":"object","additionalProperties":false,"properties":{"value":{"type":"integer","title":"Value","minimum":0,"maximum":9}}}',
			onSave: async () => '',
			onCancel: () => {}
		});
		await expect.element(page.getByLabelText('Wert')).toBeVisible();
		await expect.element(page.getByRole('heading', { name: 'Testmodul (simulate)' })).toBeVisible();
	});
});
