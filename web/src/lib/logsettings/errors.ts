import { ConnectError } from '@connectrpc/connect';
import { adminError } from '#lib/admin/admin.js';
import { Code, codeOf } from '#lib/api/client.js';
import { m } from '#lib/paraglide/messages.js';

/**
 * The message of a failed change. INVALID_ARGUMENT names the admin's own input (which rule, what
 * the regex parser says), so its text is shown.
 */
export function logSettingsError(error: unknown): string {
	if (codeOf(error) === Code.InvalidArgument)
		return m.logsettings_error_invalid({ reason: ConnectError.from(error).rawMessage });
	return adminError(error);
}
