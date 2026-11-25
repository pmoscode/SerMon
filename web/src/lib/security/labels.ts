/** Names of the security dashboard's kinds, ranges and details (KONZEPT §16.4). */
import type { SecurityEvent } from '#lib/api/gen/sermon/web/v1/security_event_pb.js';
import { formatDuration } from '#lib/format/format.js';
import { displayText } from '#lib/logs/text.js';
import { m } from '#lib/paraglide/messages.js';
import type { Locale } from '#lib/paraglide/runtime.js';
import type { SecurityPreset } from './filter.js';

export const securityPresetLabels: Record<SecurityPreset, () => string> = {
	'1h': () => m.range_1h(),
	'24h': () => m.range_24h(),
	'7d': () => m.range_7d(),
	'30d': () => m.range_30d(),
	'90d': () => m.range_90d(),
	'365d': () => m.range_365d()
};

const kinds: Record<string, () => string> = {
	'ssh.login.failed': m.secdash_kind_ssh_failed,
	'ssh.login.invalid_user': m.secdash_kind_ssh_invalid_user,
	'ssh.login.success': m.secdash_kind_ssh_success,
	'ssh.preauth.disconnect': m.secdash_kind_ssh_preauth,
	'sudo.auth.failed': m.secdash_kind_sudo_failed,
	'sudo.not_allowed': m.secdash_kind_sudo_not_allowed,
	'sudo.command': m.secdash_kind_sudo_command,
	'account.created': m.secdash_kind_account_created,
	'account.deleted': m.secdash_kind_account_deleted,
	'system.reboot': m.secdash_kind_reboot,
	'system.oom_kill': m.secdash_kind_oom_kill,
	'enrollment.token_used': m.secdash_kind_token_used,
	'node.locked': m.secdash_kind_node_locked,
	'node.rotation_new_ip': m.secdash_kind_rotation_new_ip,
	'updates.run_failed': m.secdash_kind_update_run_failed,
	'updates.rollout_stopped': m.secdash_kind_update_rollout_stopped
};

/** The name of an event kind; an unknown kind by its key. */
export function kindLabel(kind: string): string {
	return kinds[kind]?.() ?? kind;
}

/** Whether a kind is a failure (shown with an icon and in red, never by colour alone). */
export function failureKind(kind: string): boolean {
	return [
		'ssh.login.failed',
		'ssh.login.invalid_user',
		'ssh.preauth.disconnect',
		'sudo.auth.failed',
		'sudo.not_allowed'
	].includes(kind);
}

/** The remote end as one text: address or host name, with the port. */
export function remoteText(e: SecurityEvent): string {
	const host = e.remoteIp || displayText(e.remoteHost);
	if (!host) return '';
	if (!e.remotePort) return host;
	return e.remoteIp.includes(':') ? `[${host}]:${e.remotePort}` : `${host}:${e.remotePort}`;
}

/** The details of an event kind as one line of text (from the host: made visible). */
export function detailText(e: SecurityEvent, locale: Locale): string {
	const d = e.details;
	if (!d) return '';
	const parts: string[] = [];
	const add = (label: string, value: string | undefined) => {
		if (value) parts.push(`${label} ${displayText(value)}`);
	};
	switch (e.kind) {
		case 'ssh.login.failed':
		case 'ssh.login.success':
			if (d.method) parts.push(displayText(d.method));
			if (d.keyType) parts.push(displayText(d.keyType));
			if (d.keyFingerprint) parts.push(displayText(d.keyFingerprint));
			if (d.invalidUser) parts.push(m.secdash_detail_invalid_user());
			break;
		case 'sudo.auth.failed':
		case 'sudo.not_allowed':
		case 'sudo.command':
			add(m.secdash_detail_target(), d.target);
			add(m.secdash_detail_tty(), d.tty);
			add(m.secdash_detail_command(), d.command);
			break;
		case 'account.created':
			if (d.uid !== undefined) parts.push(`UID ${d.uid}`);
			break;
		case 'system.oom_kill':
			if (d.process) parts.push(`${displayText(d.process)} (PID ${d.pid})`);
			break;
		case 'system.reboot':
			if (d.uptimeBefore)
				parts.push(
					m.secdash_detail_uptime({
						uptime: formatDuration(Number(d.uptimeBefore.seconds), locale)
					})
				);
			break;
		case 'node.locked':
		case 'updates.run_failed':
		case 'updates.rollout_stopped':
			add(m.secdash_detail_reason(), d.reason);
			break;
		case 'node.rotation_new_ip':
			add(m.secdash_detail_previous_ip(), d.previousIp);
			break;
	}
	return parts.join(' · ');
}

/** The name of a country by its ISO code in the UI language; the code if the browser lacks one. */
export function countryName(code: string, locale: Locale): string {
	if (!/^[A-Z]{2}$/.test(code)) return '';
	try {
		return new Intl.DisplayNames([locale], { type: 'region' }).of(code) ?? code;
	} catch {
		return code;
	}
}
