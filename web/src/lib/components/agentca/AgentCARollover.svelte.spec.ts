import { create } from '@bufbuild/protobuf';
import { timestampFromDate } from '@bufbuild/protobuf/wkt';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { Code } from '#lib/api/client.js';
import { agentCAApi } from '#lib/api/agentca.js';
import { type AgentCA, AgentCASchema } from '#lib/api/gen/sermon/web/v1/agent_ca_pb.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { stepUpState } from '#lib/stores/stepup.svelte.js';
import { rpcError, stepUpError } from '#lib/test/fakes.js';
import AgentCARollover from './AgentCARollover.svelte';

const day = 24 * 60 * 60 * 1000;

function ca(id: number, issuing: boolean, expiresInDays: number, inUse = 0): AgentCA {
	return create(AgentCASchema, {
		id,
		issuing,
		createTime: timestampFromDate(new Date(Date.now() - 10 * day)),
		expireTime: timestampFromDate(new Date(Date.now() + (expiresInDays + 0.5) * day)),
		rootFingerprint: String(id).repeat(64),
		certificatesInUse: BigInt(inUse)
	});
}

function status(...authorities: AgentCA[]) {
	return vi.spyOn(agentCAApi, 'getAgentCAStatus').mockResolvedValue({ authorities } as never);
}

describe('AgentCARollover', () => {
	beforeEach(() => switchLocale('en'));
	afterEach(() => {
		vi.restoreAllMocks();
		stepUpState.finish(false);
	});

	// V1-19: with one CA a rollover can start; it explains itself and asks for the step-up.
	it('starts a rollover after the explanation and a step-up', async () => {
		status(ca(1, true, 400));
		const start = vi
			.spyOn(agentCAApi, 'startAgentCARollover')
			.mockRejectedValueOnce(stepUpError())
			.mockResolvedValue({} as never);
		render(AgentCARollover);

		await expect.element(page.getByText(/No rollover is running/)).toBeVisible();
		await expect.element(page.getByText(/expires on .* \(in 400 days\)/)).toBeVisible();
		await expect
			.element(page.getByRole('row', { name: /Issuing/ }))
			.toMatchTextContent(/1111111111/);
		await page.getByRole('button', { name: 'Start the rollover' }).click();
		await expect.element(page.getByText(/A new CA issues all agent certificates/)).toBeVisible();
		await page.getByRole('button', { name: 'Start the rollover' }).click();

		await vi.waitFor(() => expect(stepUpState.open).toBe(true));
		expect(start).toHaveBeenCalledTimes(1);
		stepUpState.finish(true);
		await expect.element(page.getByText(/The rollover is running/)).toBeVisible();
		expect(start).toHaveBeenCalledTimes(2);
	});

	// From 90 days before the expiry the page warns like the self-monitoring.
	it('warns before the expiry', async () => {
		status(ca(1, true, 45));
		render(AgentCARollover);
		const warning = page.getByText(/in 45 days\. After that agents and server refuse each other/);
		await expect.element(warning).toBeVisible();
		expect(warning.element().closest('[data-level]')?.getAttribute('data-level')).toBe('warning');
	});

	// During a rollover the previous CA can be retired only without valid certificates.
	it('retires the previous CA once its certificates are gone', async () => {
		const get = status(ca(2, true, 720), ca(1, false, 25, 3));
		const retire = vi.spyOn(agentCAApi, 'retireAgentCA').mockResolvedValue({} as never);
		const { unmount } = await render(AgentCARollover);
		await expect.element(page.getByText(/still has 3 valid certificates/)).toBeVisible();
		await expect
			.element(page.getByRole('button', { name: 'Retire the previous CA' }))
			.toBeDisabled();
		expect(
			page
				.getByText(/expires on/)
				.element()
				.closest('[data-level]')
				?.getAttribute('data-level')
		).toBe('critical');
		await unmount();

		get.mockResolvedValue({ authorities: [ca(2, true, 720), ca(1, false, 25, 0)] } as never);
		render(AgentCARollover);
		await page.getByRole('button', { name: 'Retire the previous CA' }).click();
		await expect.element(page.getByText(/is no longer trusted/)).toBeVisible();
		await page.getByRole('button', { name: 'Retire the previous CA' }).click();
		await expect.element(page.getByText(/The previous CA is retired/)).toBeVisible();
		expect(retire).toHaveBeenCalledWith(expect.objectContaining({ id: 1 }));
	});

	it('says why the server refused', async () => {
		status(ca(1, true, 400));
		vi.spyOn(agentCAApi, 'startAgentCARollover').mockRejectedValue(
			rpcError(Code.FailedPrecondition)
		);
		render(AgentCARollover);
		await page.getByRole('button', { name: 'Start the rollover' }).click();
		await page.getByRole('button', { name: 'Start the rollover' }).click();
		await expect
			.element(page.getByRole('alert'))
			.toMatchTextContent(/A rollover is running already/);
	});
});
