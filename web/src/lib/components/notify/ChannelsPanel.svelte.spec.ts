import { create } from '@bufbuild/protobuf';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { Code } from '#lib/api/client.js';
import { alertingApi } from '#lib/api/alerting.js';
import {
	ChannelFieldKind,
	DeliveryStatus,
	ListChannelTypesResponseSchema,
	ListChannelsResponseSchema,
	NotificationChannelSchema
} from '#lib/api/gen/sermon/web/v1/notification_pb.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { stepUpState } from '#lib/stores/stepup.svelte.js';
import { rpcError, stepUpError } from '#lib/test/fakes.js';
import ChannelsPanel from './ChannelsPanel.svelte';

const TOKEN = 'tk_fixture_secret_0815'; // betterleaks:allow

const types = create(ListChannelTypesResponseSchema, {
	types: [
		{
			type: 'ntfy',
			fields: [
				{ name: 'server', kind: ChannelFieldKind.URL, required: true, maxLength: 2048 },
				{ name: 'topic', kind: ChannelFieldKind.TEXT, secret: true, required: true, maxLength: 64 },
				{ name: 'token', kind: ChannelFieldKind.TEXT, secret: true, maxLength: 256 }
			]
		}
	]
});
const ops = create(NotificationChannelSchema, {
	id: 'c1',
	type: 'ntfy',
	name: 'Ops',
	language: 'en',
	enabled: true,
	settings: { server: 'https://ntfy.example' },
	secretNames: ['token', 'topic']
});

describe('ChannelsPanel', () => {
	beforeEach(() => {
		switchLocale('en');
		vi.spyOn(alertingApi.channels, 'listChannelTypes').mockResolvedValue(types);
	});
	afterEach(() => {
		vi.restoreAllMocks();
		if (stepUpState.open) stepUpState.finish(false);
	});

	it('creates a channel after the step-up and never shows its secrets', async () => {
		vi.spyOn(alertingApi.channels, 'listChannels')
			.mockResolvedValueOnce(create(ListChannelsResponseSchema, { channels: [] }))
			.mockResolvedValue(create(ListChannelsResponseSchema, { channels: [ops] }));
		const createChannel = vi
			.spyOn(alertingApi.channels, 'createChannel')
			.mockRejectedValueOnce(stepUpError())
			.mockResolvedValueOnce({} as never);
		render(ChannelsPanel);

		await page.getByRole('button', { name: 'New channel' }).click();
		await page.getByLabelText('Name').fill('Ops');
		await page.getByLabelText('Server').fill('https://ntfy.example');
		await page.getByLabelText('Topic').fill('sermon-ops');
		await page.getByLabelText('Token (optional)').fill(TOKEN);
		await expect
			.element(page.getByLabelText('Token (optional)'))
			.toHaveAttribute('type', 'password');
		await page.getByRole('button', { name: 'Create channel' }).click();
		await vi.waitFor(() => expect(stepUpState.open).toBe(true));
		stepUpState.finish(true);

		await expect.element(page.getByText('Set: Token, Topic')).toBeVisible();
		expect(createChannel).toHaveBeenCalledTimes(2);
		expect(createChannel.mock.calls[1][0]).toEqual({
			type: 'ntfy',
			name: 'Ops',
			language: 'de',
			enabled: true,
			settings: { server: 'https://ntfy.example' },
			secrets: { topic: 'sermon-ops', token: TOKEN }
		});
		expect(document.body.textContent).not.toContain(TOKEN);
		expect(document.body.innerHTML).not.toContain(TOKEN);
	});

	it('keeps stored secrets when editing and sends only what was typed', async () => {
		vi.spyOn(alertingApi.channels, 'listChannels').mockResolvedValue(
			create(ListChannelsResponseSchema, { channels: [ops] })
		);
		const update = vi.spyOn(alertingApi.channels, 'updateChannel').mockResolvedValue({} as never);
		render(ChannelsPanel);

		await page.getByRole('button', { name: 'Edit' }).click();
		// The stored token is not in the form, only the hint that it is set.
		await expect.element(page.getByLabelText('Token (optional)')).toHaveValue('');
		await expect
			.element(page.getByLabelText('Token (optional)'))
			.toHaveAttribute('placeholder', 'set – leave empty to keep');
		await page.getByLabelText('Name').fill('Ops night');
		await page.getByRole('button', { name: 'Save' }).click();

		await vi.waitFor(() => expect(update).toHaveBeenCalledOnce());
		expect(update.mock.calls[0][0]).toEqual({
			channelId: 'c1',
			name: 'Ops night',
			language: 'en',
			enabled: true,
			replaceSettings: true,
			settings: { server: 'https://ntfy.example' },
			secrets: {}
		});
	});

	it('shows the error of a failed test and of an invalid input', async () => {
		vi.spyOn(alertingApi.channels, 'listChannels').mockResolvedValue(
			create(ListChannelsResponseSchema, { channels: [ops] })
		);
		vi.spyOn(alertingApi.channels, 'testChannel')
			.mockResolvedValueOnce({
				delivery: {
					status: DeliveryStatus.FAILED,
					error: 'POST ntfy.example: HTTP 404: topic not found'
				}
			} as never)
			.mockRejectedValueOnce(rpcError(Code.ResourceExhausted));
		render(ChannelsPanel);

		await page.getByRole('button', { name: 'Send test' }).click();
		await expect
			.element(page.getByRole('alert'))
			.toHaveTextContent('Test failed: POST ntfy.example: HTTP 404: topic not found');
		await page.getByRole('button', { name: 'Send test' }).click();
		await expect
			.element(page.getByRole('alert'))
			.toHaveTextContent('Too many tests in a short time. Please wait a minute.');
	});

	it('deletes after a second click and switches off', async () => {
		vi.spyOn(alertingApi.channels, 'listChannels').mockResolvedValue(
			create(ListChannelsResponseSchema, { channels: [ops] })
		);
		const remove = vi.spyOn(alertingApi.channels, 'deleteChannel').mockResolvedValue({} as never);
		const update = vi.spyOn(alertingApi.channels, 'updateChannel').mockResolvedValue({} as never);
		render(ChannelsPanel);

		await page.getByRole('button', { name: 'Switch off' }).click();
		await vi.waitFor(() =>
			expect(update).toHaveBeenCalledWith({ channelId: 'c1', enabled: false })
		);
		await page.getByRole('button', { name: 'Delete', exact: true }).click();
		expect(remove).not.toHaveBeenCalled();
		await page.getByRole('button', { name: 'Really delete' }).click();
		await vi.waitFor(() => expect(remove).toHaveBeenCalledWith({ channelId: 'c1' }));
	});
});
