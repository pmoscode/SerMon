import { create } from '@bufbuild/protobuf';
import { timestampFromMs } from '@bufbuild/protobuf/wkt';
import { Code, ConnectError } from '@connectrpc/connect';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { SetUpdatesPausedResponseSchema } from '#lib/api/gen/sermon/web/v1/update_policy_pb.js';
import { updatesApi } from '#lib/api/updates.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { sessionState } from '#lib/stores/session.svelte.js';
import { stepUpState } from '#lib/stores/stepup.svelte.js';
import { fakeSession, stepUpError } from '#lib/test/fakes.js';
import UpdatesPause from './UpdatesPause.svelte';

const since = Date.UTC(2026, 10, 22, 9, 30, 0);

function as(orgAdmin: boolean) {
	sessionState.current = { ...fakeSession(), orgAdmin };
	sessionState.status = 'authenticated';
}

const formatted = (locale: string) =>
	new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(
		new Date(since)
	);

describe('UpdatesPause', () => {
	beforeEach(() => switchLocale('en'));
	afterEach(() => {
		vi.restoreAllMocks();
		if (stepUpState.open) stepUpState.finish(false);
		sessionState.current = undefined;
		sessionState.status = 'unknown';
	});

	it('shows nothing to a member while updates run', async () => {
		as(false);
		const { container } = await render(UpdatesPause, { onchange: async () => {} });
		await new Promise((r) => setTimeout(r, 20));
		expect(container.textContent?.trim()).toBe('');
		expect(page.getByRole('button').elements()).toHaveLength(0);
	});

	it('tells everyone since when updates are paused', async () => {
		as(false);
		render(UpdatesPause, { pausedSince: timestampFromMs(since), onchange: async () => {} });
		const banner = page.getByText(/Updates are paused since/);
		await expect.element(banner).toBeVisible();
		expect(banner.element().textContent).toContain(`Updates are paused since ${formatted('en')}.`);
		expect(page.getByRole('button').elements()).toHaveLength(0);
	});

	it('says it in German', async () => {
		as(false);
		switchLocale('de');
		render(UpdatesPause, { pausedSince: timestampFromMs(since), onchange: async () => {} });
		await expect.element(page.getByText(/Updates sind pausiert seit/)).toBeVisible();
		expect(document.body.textContent).toContain(`Updates sind pausiert seit ${formatted('de')}.`);
	});

	it('lets an admin pause after a confirmation and a step-up', async () => {
		as(true);
		const onchange = vi.fn(async () => {});
		const pause = vi
			.spyOn(updatesApi.policies, 'setUpdatesPaused')
			.mockRejectedValueOnce(stepUpError())
			.mockResolvedValueOnce(
				create(SetUpdatesPausedResponseSchema, { pausedSince: timestampFromMs(since) })
			);
		render(UpdatesPause, { onchange });
		await page.getByRole('button', { name: 'Pause updates' }).click();
		expect(pause).not.toHaveBeenCalled();
		const confirm = page.getByRole('group', { name: 'Pause all updates of the organization?' });
		expect(confirm.element().textContent).toContain('updates and reboots by hand are refused');
		await confirm.getByRole('button', { name: 'Pause updates' }).click();
		await vi.waitFor(() => expect(stepUpState.open).toBe(true));
		stepUpState.finish(true);
		await vi.waitFor(() => expect(pause).toHaveBeenCalledTimes(2));
		expect(pause.mock.calls[1][0]).toEqual({ paused: true });
		await vi.waitFor(() => expect(onchange).toHaveBeenCalled());
		await expect.element(confirm).not.toBeInTheDocument();
	});

	it('lets an admin change their mind about pausing', async () => {
		as(true);
		const pause = vi.spyOn(updatesApi.policies, 'setUpdatesPaused');
		render(UpdatesPause, { onchange: async () => {} });
		await page.getByRole('button', { name: 'Pause updates' }).click();
		await page.getByRole('button', { name: 'Cancel' }).click();
		await expect.element(page.getByRole('group')).not.toBeInTheDocument();
		expect(pause).not.toHaveBeenCalled();
	});

	it('lets an admin resume', async () => {
		as(true);
		const onchange = vi.fn(async () => {});
		const resume = vi
			.spyOn(updatesApi.policies, 'setUpdatesPaused')
			.mockResolvedValue(create(SetUpdatesPausedResponseSchema, {}));
		render(UpdatesPause, { pausedSince: timestampFromMs(since), onchange });
		await page.getByRole('button', { name: 'Resume updates' }).click();
		await vi.waitFor(() => expect(resume).toHaveBeenCalledWith({ paused: false }));
		await vi.waitFor(() => expect(onchange).toHaveBeenCalled());
	});

	it('says why it failed', async () => {
		as(true);
		vi.spyOn(updatesApi.policies, 'setUpdatesPaused').mockRejectedValue(
			new ConnectError('forbidden', Code.PermissionDenied)
		);
		render(UpdatesPause, { pausedSince: timestampFromMs(since), onchange: async () => {} });
		await page.getByRole('button', { name: 'Resume updates' }).click();
		await expect.element(page.getByRole('alert')).toMatchTextContent(/lack the rights/);
	});
});
