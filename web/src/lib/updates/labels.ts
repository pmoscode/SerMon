/** Texts of the update states (M4-15); every state also has a word, never a colour alone. */
import {
	RolloutNodeState,
	RolloutState,
	UpdateRunKind,
	UpdateRunState
} from '#lib/api/gen/sermon/web/v1/update_pb.js';
import { UpdateStatus } from '#lib/api/gen/sermon/web/v1/update_status_pb.js';
import { AgentChannel, UpdateMode } from '#lib/api/gen/sermon/web/v1/update_policy_pb.js';
import { m } from '#lib/paraglide/messages.js';

export function statusLabel(s: UpdateStatus): string {
	switch (s) {
		case UpdateStatus.SECURITY:
			return m.status_short_security();
		case UpdateStatus.UPDATES:
			return m.status_short_updates();
		case UpdateStatus.CURRENT:
			return m.status_short_current();
		case UpdateStatus.UNAVAILABLE:
			return m.status_short_unavailable();
		default:
			return m.status_short_unknown();
	}
}

export function rolloutLabel(s: RolloutState): string {
	return s === RolloutState.DONE
		? m.rollout_done()
		: s === RolloutState.STOPPED
			? m.rollout_stopped()
			: m.rollout_running();
}

const nodeStates: Record<number, () => string> = {
	[RolloutNodeState.WAITING]: m.rnode_waiting,
	[RolloutNodeState.UPDATING]: m.rnode_updating,
	[RolloutNodeState.REBOOTING]: m.rnode_rebooting,
	[RolloutNodeState.HEALTH_CHECK]: m.rnode_health,
	[RolloutNodeState.DONE]: m.rnode_done,
	[RolloutNodeState.FAILED]: m.rnode_failed,
	[RolloutNodeState.SKIPPED]: m.rnode_skipped,
	[RolloutNodeState.AGENT_UPDATING]: m.rnode_agent
};

export function rolloutNodeLabel(s: RolloutNodeState): string {
	return nodeStates[s]?.() ?? '–';
}

export function modeLabel(mode: UpdateMode): string {
	return mode === UpdateMode.SECURITY_AUTO
		? m.mode_security()
		: mode === UpdateMode.ALL_AUTO
			? m.mode_all()
			: m.mode_report();
}

export function runKindLabel(k: UpdateRunKind): string {
	return k === UpdateRunKind.REBOOT
		? m.runs_kind_reboot()
		: k === UpdateRunKind.SELF_UPDATE
			? m.runs_kind_self_update()
			: m.runs_kind_update();
}

const runStates: Record<number, () => string> = {
	[UpdateRunState.PENDING]: m.runs_state_pending,
	[UpdateRunState.SENT]: m.runs_state_sent,
	[UpdateRunState.RUNNING]: m.runs_state_running,
	[UpdateRunState.SUCCEEDED]: m.runs_state_succeeded,
	[UpdateRunState.FAILED]: m.runs_state_failed,
	[UpdateRunState.REFUSED]: m.runs_state_refused,
	[UpdateRunState.UNSUPPORTED]: m.runs_state_unsupported,
	[UpdateRunState.LOST]: m.runs_state_lost,
	[UpdateRunState.EXPIRED]: m.runs_state_expired
};

export function runStateLabel(s: UpdateRunState): string {
	return runStates[s]?.() ?? '–';
}

/** Whether a run state is a failure (shown with an icon and the word). */
export function runFailed(s: UpdateRunState): boolean {
	return [
		UpdateRunState.FAILED,
		UpdateRunState.REFUSED,
		UpdateRunState.UNSUPPORTED,
		UpdateRunState.LOST,
		UpdateRunState.EXPIRED
	].includes(s);
}

export function channelLabel(c: AgentChannel): string {
	return c === AgentChannel.BETA ? m.channel_beta() : m.channel_stable();
}
