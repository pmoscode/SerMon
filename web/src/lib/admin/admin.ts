import { Code, codeOf } from '#lib/api/client.js';
import { Role } from '#lib/api/gen/sermon/web/v1/access_pb.js';
import { needsStepUp } from '#lib/stores/stepup.svelte.js';
import { m } from '#lib/paraglide/messages.js';

/** The roles that can be granted, lowest first. */
export const roles = [Role.VIEWER, Role.OPERATOR, Role.ADMIN] as const;

export function roleName(role: Role): string {
	switch (role) {
		case Role.VIEWER:
			return m.role_viewer();
		case Role.OPERATOR:
			return m.role_operator();
		case Role.ADMIN:
			return m.role_admin();
		default:
			return '–';
	}
}

/**
 * The message for a failed admin action; empty for a cancelled step-up (nothing went wrong).
 * FAILED_PRECONDITION without step-up detail is the last admin or the own second factor.
 */
export function adminError(error: unknown, precondition = m.admin_error_last_admin()): string {
	if (needsStepUp(error)) return '';
	switch (codeOf(error)) {
		case Code.PermissionDenied:
			return m.admin_error_forbidden();
		case Code.AlreadyExists:
			return m.admin_error_exists();
		case Code.InvalidArgument:
			return m.admin_error_invalid();
		case Code.NotFound:
			return m.admin_error_gone();
		case Code.FailedPrecondition:
			return precondition;
		default:
			return m.error_unavailable();
	}
}

/** Copies text to the clipboard; false if the browser refuses. */
export async function copyText(text: string): Promise<boolean> {
	try {
		await navigator.clipboard.writeText(text);
		return true;
	} catch {
		return false;
	}
}
