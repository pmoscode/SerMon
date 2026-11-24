import { create } from '@bufbuild/protobuf';
import { timestampFromMs } from '@bufbuild/protobuf/wkt';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { api, Code } from '#lib/api/client.js';
import { adminApi } from '#lib/api/admin.js';
import { ListPasskeysResponseSchema } from '#lib/api/gen/sermon/web/v1/passkey_pb.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { sessionState } from '#lib/stores/session.svelte.js';
import { stepUpState } from '#lib/stores/stepup.svelte.js';
import { fakeSession, rpcError, stepUpError } from '#lib/test/fakes.js';
import { creationJson, fakeAttestation, notAllowed } from '#lib/test/webauthn.js';
import PasskeySettings from './PasskeySettings.svelte';

const hostile = '<img src=x onerror=alert(1)>';
const listing = (...names: string[]) =>
	create(ListPasskeysResponseSchema, {
		passkeys: names.map((name, i) => ({
			id: `id${i}`,
			name,
			createTime: timestampFromMs(Date.parse('2026-11-01T10:00:00Z')),
			synced: i === 0
		}))
	});

describe('PasskeySettings', () => {
	beforeEach(() => {
		switchLocale('en');
		sessionState.current = { ...fakeSession(), passkeysAvailable: true };
		vi.spyOn(api.auth, 'getSession').mockResolvedValue({ session: sessionState.current } as never);
	});
	afterEach(() => {
		vi.restoreAllMocks();
		if (stepUpState.open) stepUpState.finish(false);
		sessionState.current = undefined;
	});

	it('adds a passkey after the step-up and lists names as text', async () => {
		const list = vi
			.spyOn(adminApi.passkeys, 'listPasskeys')
			.mockResolvedValueOnce(listing())
			.mockResolvedValue(listing(hostile));
		const begin = vi
			.spyOn(adminApi.passkeys, 'beginPasskeyRegistration')
			.mockRejectedValueOnce(stepUpError())
			.mockResolvedValue({ ceremonyId: 'c1', optionsJson: creationJson } as never);
		const createCred = vi
			.spyOn(navigator.credentials, 'create')
			.mockResolvedValue(fakeAttestation());
		const finish = vi
			.spyOn(adminApi.passkeys, 'finishPasskeyRegistration')
			.mockResolvedValue({} as never);
		render(PasskeySettings);
		await expect.element(page.getByText('No passkey yet.')).toBeVisible();
		await page.getByLabelText('Name of the passkey').fill(' Laptop ');
		await page.getByRole('button', { name: 'Add a passkey' }).click();
		await vi.waitFor(() => expect(stepUpState.open).toBe(true));
		stepUpState.finish(true);
		await vi.waitFor(() => expect(finish).toHaveBeenCalled());
		expect(begin).toHaveBeenLastCalledWith({ name: 'Laptop' });
		const options = createCred.mock.calls[0][0]!.publicKey!;
		expect(new Uint8Array(options.challenge as ArrayBuffer)).toEqual(new Uint8Array([1, 2, 3]));
		const sent = JSON.parse(finish.mock.calls[0][0].credentialJson as string);
		expect(sent).toMatchObject({
			rawId: 'AQI',
			type: 'public-key',
			response: { attestationObject: 'BA', transports: ['internal'] }
		});
		expect(finish.mock.calls[0][0].ceremonyId).toBe('c1');
		await expect.element(page.getByText(hostile)).toBeVisible();
		await expect.element(page.getByText('synced')).toBeVisible();
		expect(document.querySelector('img')).toBeNull();
		expect(list).toHaveBeenCalledTimes(2);
	});

	it('says nothing when the user cancels, and explains refusals', async () => {
		vi.spyOn(adminApi.passkeys, 'listPasskeys').mockResolvedValue(listing());
		vi.spyOn(adminApi.passkeys, 'beginPasskeyRegistration').mockResolvedValue({
			ceremonyId: 'c1',
			optionsJson: creationJson
		} as never);
		vi.spyOn(navigator.credentials, 'create')
			.mockRejectedValueOnce(notAllowed())
			.mockResolvedValue(fakeAttestation());
		vi.spyOn(adminApi.passkeys, 'finishPasskeyRegistration').mockRejectedValue(
			rpcError(Code.ResourceExhausted)
		);
		render(PasskeySettings);
		await page.getByLabelText('Name of the passkey').fill('Key');
		await page.getByRole('button', { name: 'Add a passkey' }).click();
		await expect.element(page.getByRole('button', { name: 'Add a passkey' })).toBeEnabled();
		expect(page.getByRole('alert').elements()).toHaveLength(0);
		await page.getByRole('button', { name: 'Add a passkey' }).click();
		await expect
			.element(page.getByRole('alert'))
			.toHaveTextContent('You have 10 passkeys already. Delete one first.');
	});

	it('renames and deletes after the step-up', async () => {
		vi.spyOn(adminApi.passkeys, 'listPasskeys').mockResolvedValue(listing('Laptop'));
		const rename = vi.spyOn(adminApi.passkeys, 'renamePasskey').mockResolvedValue({} as never);
		const del = vi.spyOn(adminApi.passkeys, 'deletePasskey').mockResolvedValue({} as never);
		render(PasskeySettings);
		await page.getByRole('button', { name: 'Rename passkey Laptop' }).click();
		await page.getByLabelText('Name of the passkey', { exact: true }).first().fill('Work');
		await page.getByRole('button', { name: 'Save' }).click();
		await vi.waitFor(() => expect(rename).toHaveBeenCalledWith({ id: 'id0', name: 'Work' }));
		await page.getByRole('button', { name: 'Delete passkey Laptop' }).click();
		await page.getByRole('button', { name: 'Really delete' }).click();
		await vi.waitFor(() => expect(del).toHaveBeenCalledWith({ id: 'id0' }));
	});

	it('stays away without passkeys on the server', async () => {
		sessionState.current = { ...fakeSession(), passkeysAvailable: false };
		const list = vi.spyOn(adminApi.passkeys, 'listPasskeys').mockResolvedValue(listing('Laptop'));
		render(PasskeySettings);
		await vi.waitFor(() => expect(list).toHaveBeenCalled());
		expect(page.getByText('Passkeys').elements()).toHaveLength(0);
	});
});
