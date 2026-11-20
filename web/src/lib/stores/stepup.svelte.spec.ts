import { afterEach, describe, expect, it, vi } from 'vitest';
import { Code } from '#lib/api/client.js';
import { rpcError, stepUpError } from '#lib/test/fakes.js';
import { needsStepUp, stepUpState, withStepUp } from './stepup.svelte.js';

describe('withStepUp', () => {
	afterEach(() => {
		if (stepUpState.open) stepUpState.finish(false);
	});

	it('recognizes only the step-up answer, not other failed preconditions', () => {
		expect(needsStepUp(stepUpError())).toBe(true);
		expect(needsStepUp(rpcError(Code.FailedPrecondition))).toBe(false);
		expect(needsStepUp(rpcError(Code.PermissionDenied))).toBe(false);
	});

	it('asks for the password and retries the action once', async () => {
		const action = vi.fn().mockRejectedValueOnce(stepUpError()).mockResolvedValueOnce('done');

		const result = withStepUp(action);
		await vi.waitFor(() => expect(stepUpState.open).toBe(true));
		stepUpState.finish(true);

		await expect(result).resolves.toBe('done');
		expect(action).toHaveBeenCalledTimes(2);
		expect(stepUpState.open).toBe(false);
	});

	it('rethrows when the dialog is cancelled', async () => {
		const original = stepUpError();
		const action = vi.fn().mockRejectedValue(original);

		const result = withStepUp(action);
		await vi.waitFor(() => expect(stepUpState.open).toBe(true));
		stepUpState.finish(false);

		await expect(result).rejects.toBe(original);
		expect(action).toHaveBeenCalledOnce();
	});

	it('passes other errors through without asking', async () => {
		const action = vi.fn().mockRejectedValue(rpcError(Code.NotFound));

		await expect(withStepUp(action)).rejects.toThrow();
		expect(stepUpState.open).toBe(false);
	});

	it('shares one dialog between concurrent actions', async () => {
		const a = withStepUp(vi.fn().mockRejectedValueOnce(stepUpError()).mockResolvedValueOnce('a'));
		const b = withStepUp(vi.fn().mockRejectedValueOnce(stepUpError()).mockResolvedValueOnce('b'));
		await vi.waitFor(() => expect(stepUpState.open).toBe(true));

		stepUpState.finish(true);

		await expect(Promise.all([a, b])).resolves.toEqual(['a', 'b']);
	});
});
