import { create } from '@bufbuild/protobuf';
import { timestampFromMs } from '@bufbuild/protobuf/wkt';
import { Code, ConnectError } from '@connectrpc/connect';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import {
	GetUpdateRunResponseSchema,
	ListUpdateRunsResponseSchema,
	UpdateRunKind,
	UpdateRunState,
	UpdateScope
} from '#lib/api/gen/sermon/web/v1/update_pb.js';
import { updatesApi } from '#lib/api/updates.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import RunHistory from './RunHistory.svelte';

const now = Date.UTC(2026, 10, 22, 12, 0, 0);
const nodeId = '0193f5a0-0000-7000-8000-0000000000d1';

function runs() {
	return create(ListUpdateRunsResponseSchema, {
		runs: [
			{
				runId: 'r1',
				nodeId,
				kind: UpdateRunKind.UPDATE,
				scope: UpdateScope.SECURITY,
				state: UpdateRunState.FAILED,
				exitCode: 100,
				resultReason: 'dpkg was interrupted',
				createdBy: 'u1',
				createTime: timestampFromMs(now - 600_000)
			},
			{
				runId: 'r2',
				nodeId,
				kind: UpdateRunKind.REBOOT,
				state: UpdateRunState.REFUSED,
				reason: 'kernel',
				createTime: timestampFromMs(now - 3_600_000)
			}
		]
	});
}

describe('RunHistory', () => {
	beforeEach(() => switchLocale('en'));
	afterEach(() => vi.restoreAllMocks());

	it('lists the runs with state, exit code and who asked', async () => {
		vi.spyOn(updatesApi.updates, 'listUpdateRuns').mockResolvedValue(runs());
		render(RunHistory, { nodeId, now: new Date(now) });
		await expect.element(page.getByText('Updates (security)')).toBeVisible();
		await expect.element(page.getByText(/failed · exit code 100/)).toBeVisible();
		await expect.element(page.getByText('dpkg was interrupted')).toBeVisible();
		await expect.element(page.getByText(/by hand · 10 minutes ago/)).toBeVisible();
		await expect.element(page.getByText('refused by the host')).toBeVisible();
		await expect.element(page.getByText(/by policy/)).toBeVisible();
	});

	it('shows the output as text with control characters marked', async () => {
		vi.spyOn(updatesApi.updates, 'listUpdateRuns').mockResolvedValue(runs());
		const get = vi.spyOn(updatesApi.updates, 'getUpdateRun').mockResolvedValue(
			create(GetUpdateRunResponseSchema, {
				run: {
					runId: 'r1',
					output: 'Setting up <b>openssl</b>\r\x1b]0;title\x07\u202edone',
					outputTruncated: true
				}
			})
		);
		render(RunHistory, { nodeId, now: new Date(now) });
		await page.getByRole('button', { name: 'Output' }).first().click();
		await vi.waitFor(() => expect(get).toHaveBeenCalledWith({ runId: 'r1' }));
		const output = page.getByLabelText('Output');
		await expect
			.element(output)
			.toMatchTextContent(/Setting up <b>openssl<\/b>␍⟨OSC 0;title⟩⟨U\+202E⟩done/);
		expect(document.querySelector('pre b')).toBeNull();
		await expect.element(page.getByText('The beginning of the output was cut.')).toBeVisible();
		await page.getByRole('button', { name: 'Hide output' }).click();
		await expect.element(output).not.toBeInTheDocument();
	});

	it('says why the output stays hidden without the right', async () => {
		vi.spyOn(updatesApi.updates, 'listUpdateRuns').mockResolvedValue(runs());
		vi.spyOn(updatesApi.updates, 'getUpdateRun').mockRejectedValue(
			new ConnectError('forbidden', Code.PermissionDenied)
		);
		render(RunHistory, { nodeId, now: new Date(now) });
		await page.getByRole('button', { name: 'Output' }).first().click();
		await expect.element(page.getByRole('alert')).toMatchTextContent(/like a log of the host/);
	});

	// M4-31: a slow answer for a run opened before never shows under the one open now.
	it('shows the output of the open run, whatever answers last', async () => {
		vi.spyOn(updatesApi.updates, 'listUpdateRuns').mockResolvedValue(runs());
		let answerFirst: (v: unknown) => void = () => {};
		vi.spyOn(updatesApi.updates, 'getUpdateRun').mockImplementation((({
			runId
		}: {
			runId: string;
		}) =>
			runId === 'r1'
				? new Promise((resolve) => (answerFirst = resolve))
				: Promise.resolve(
						create(GetUpdateRunResponseSchema, { run: { runId: 'r2', output: 'second run' } })
					)) as never);
		render(RunHistory, { nodeId, now: new Date(now) });
		await page.getByRole('button', { name: 'Output' }).first().click();
		await page.getByRole('button', { name: 'Output' }).last().click();
		await expect.element(page.getByLabelText('Output')).toMatchTextContent(/second run/);
		answerFirst(create(GetUpdateRunResponseSchema, { run: { runId: 'r1', output: 'first run' } }));
		await new Promise((r) => setTimeout(r, 50));
		expect(page.getByLabelText('Output').element().textContent).toContain('second run');
		expect(document.body.textContent).not.toContain('first run');
	});

	// M4-31: the list of a node shown before doesn't replace the one of the node shown now.
	it('lists the runs of the node shown now', async () => {
		const other = '0193f5a0-0000-7000-8000-0000000000d2';
		let answerFirst: (v: unknown) => void = () => {};
		vi.spyOn(updatesApi.updates, 'listUpdateRuns').mockImplementation((({
			nodeId: asked
		}: {
			nodeId: string;
		}) =>
			asked === nodeId
				? new Promise((resolve) => (answerFirst = resolve))
				: Promise.resolve(create(ListUpdateRunsResponseSchema, { runs: [] }))) as never);
		const { rerender } = await render(RunHistory, { nodeId, now: new Date(now) });
		await rerender({ nodeId: other });
		await expect.element(page.getByText('No runs yet.')).toBeVisible();
		answerFirst(runs());
		await new Promise((r) => setTimeout(r, 50));
		expect(document.body.textContent).not.toContain('Updates (security)');
	});
});
