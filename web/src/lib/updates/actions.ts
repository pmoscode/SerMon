/** Rights and error texts of the update actions (M4-14, M4-31). */
import { Code, codeOf } from '#lib/api/client.js';
import { notifyError } from '#lib/notify/notify.js';
import { needsStepUp } from '#lib/stores/stepup.svelte.js';

/** Whether a right given for these groups ("*" is all) covers the group. */
export function allowedIn(groups: readonly string[] | undefined, groupId: string): boolean {
	return (groups ?? []).includes('*') || (groups ?? []).includes(groupId);
}

/**
 * The message of a failed action; `precondition` for a FAILED_PRECONDITION that is no step-up
 * (e.g. paused updates, a run that started already).
 */
export function actionError(error: unknown, precondition: string): string {
	if (codeOf(error) === Code.FailedPrecondition && !needsStepUp(error)) return precondition;
	return notifyError(error);
}
