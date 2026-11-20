import { ConnectError } from '@connectrpc/connect';
import { Code } from '#lib/api/client.js';
import { StepUpRequiredSchema } from '#lib/api/gen/sermon/web/v1/auth_pb.js';

/** Whether an error asks for a fresh re-authentication (FAILED_PRECONDITION with StepUpRequired). */
export function needsStepUp(error: unknown): boolean {
	const err = ConnectError.from(error);
	return err.code === Code.FailedPrecondition && err.findDetails(StepUpRequiredSchema).length > 0;
}

/** The password dialog for sensitive actions; StepUpDialog shows it. */
class StepUpState {
	open = $state(false);
	#pending: Promise<boolean> | undefined;
	#resolve: ((confirmed: boolean) => void) | undefined;

	/** Asks for the password; true once confirmed, false when cancelled. Concurrent asks share one dialog. */
	request(): Promise<boolean> {
		this.#pending ??= new Promise((resolve) => {
			this.#resolve = resolve;
			this.open = true;
		});
		return this.#pending;
	}

	finish(confirmed: boolean): void {
		this.open = false;
		this.#resolve?.(confirmed);
		this.#resolve = undefined;
		this.#pending = undefined;
	}
}

export const stepUpState = new StepUpState();

/**
 * Runs a sensitive action; if the server asks for a step-up, asks for the password and runs it once
 * more. Cancelling the dialog rethrows the original error.
 */
export async function withStepUp<T>(action: () => Promise<T>): Promise<T> {
	try {
		return await action();
	} catch (error) {
		if (!needsStepUp(error) || !(await stepUpState.request())) throw error;
		return action();
	}
}
