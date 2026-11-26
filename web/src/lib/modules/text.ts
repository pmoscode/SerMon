/** The texts of the module views: problems of the form and the states of a module. */
import { ConnectError } from '@connectrpc/connect';
import { adminError } from '#lib/admin/admin.js';
import { Code, codeOf } from '#lib/api/client.js';
import { ModuleState } from '#lib/api/gen/sermon/web/v1/module_pb.js';
import { m } from '#lib/paraglide/messages.js';
import type { Problem } from './schema.js';

/** The text of a problem of a field. */
export function problemText(p: Problem): string {
	switch (p.kind) {
		case 'required':
			return m.modules_problem_required();
		case 'length':
			return m.modules_problem_length({ min: p.min, max: p.max });
		case 'pattern':
			return m.modules_problem_pattern();
		case 'value':
			return m.modules_problem_value();
		case 'integer':
			return m.modules_problem_integer();
		case 'number':
			return m.modules_problem_number();
		case 'range':
			return m.modules_problem_range({ min: p.min, max: p.max });
		case 'items':
			return m.modules_problem_items({ max: p.max });
		case 'item':
			return m.modules_problem_item({ index: p.index, problem: problemText(p.problem) });
		case 'format':
			switch (p.format) {
				case 'uri':
					return m.modules_problem_uri();
				case 'host-port':
					return m.modules_problem_host_port();
				case 'path':
					return m.modules_problem_path();
				case 'duration':
					return m.modules_problem_duration();
			}
	}
}

/** The text of a module's state; unspecified: no report yet. */
export function stateText(state: ModuleState): string {
	switch (state) {
		case ModuleState.OK:
			return m.modules_state_ok();
		case ModuleState.ERROR:
			return m.modules_state_error();
		case ModuleState.TIMEOUT:
			return m.modules_state_timeout();
		case ModuleState.HUNG:
			return m.modules_state_hung();
		case ModuleState.CRASHED:
			return m.modules_state_crashed();
		case ModuleState.INVALID_CONFIG:
			return m.modules_state_invalid_config();
		case ModuleState.VETOED:
			return m.modules_state_vetoed();
		default:
			return m.modules_state_waiting();
	}
}

/** Whether a state needs attention. */
export const failing = (state: ModuleState) =>
	state !== ModuleState.OK && state !== ModuleState.UNSPECIFIED;

/**
 * The message of a failed change. INVALID_ARGUMENT names the admin's own input (which property
 * and why, in the agent's words), so its text is shown.
 */
export function modulesError(error: unknown): string {
	if (codeOf(error) === Code.InvalidArgument)
		return m.modules_error_invalid({ reason: ConnectError.from(error).rawMessage });
	return adminError(error);
}
