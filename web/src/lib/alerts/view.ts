/**
 * Pure helpers of the alert page (M2-16): which bucket a state is in, filters, counts, rule
 * names in the UI language and values in the units of the rules (app/notify).
 */
import { AlertState, type Alert } from '#lib/api/gen/sermon/web/v1/alert_pb.js';
import { formatNumber, formatValue } from '#lib/format/format.js';
import { m } from '#lib/paraglide/messages.js';
import type { Locale } from '#lib/paraglide/runtime.js';

export type Kind = 'firing' | 'pending' | 'resolved';

export interface Filter {
	state: Kind | '';
	group: string;
	search: string;
}

export function kindOf(a: Alert): Kind {
	switch (a.state) {
		case AlertState.WARNING:
		case AlertState.CRITICAL:
			return 'firing';
		case AlertState.PENDING:
			return 'pending';
		default:
			return 'resolved';
	}
}

export interface Counts {
	firing: number;
	pending: number;
	resolved: number;
	critical: number;
}

export function countAlerts(list: Alert[]): Counts {
	const counts: Counts = { firing: 0, pending: 0, resolved: 0, critical: 0 };
	for (const a of list) {
		counts[kindOf(a)]++;
		if (a.state === AlertState.CRITICAL) counts.critical++;
	}
	return counts;
}

/** The rule's name in the language; the rule ID if the rule is gone. */
export function ruleName(a: Alert, locale: Locale): string {
	const name = locale === 'de' ? a.ruleName?.de : a.ruleName?.en;
	return name || a.key?.ruleId || '';
}

/** The dimension as "mountpoint: /var" (host data, shown as text). */
export function dimensionText(a: Alert): string {
	return Object.entries(a.dimensionLabels)
		.map(([k, v]) => `${k}: ${v}`)
		.join(', ');
}

export function filterAlerts(list: Alert[], f: Filter): Alert[] {
	const search = f.search.trim().toLowerCase();
	return list.filter((a) => {
		if (f.state && kindOf(a) !== f.state) return false;
		if (f.group && a.groupId !== f.group) return false;
		if (!search) return true;
		const text = [a.hostname, a.key?.ruleId, a.ruleName?.de, a.ruleName?.en, dimensionText(a)]
			.join(' ')
			.toLowerCase();
		return text.includes(search);
	});
}

export function groupsOf(list: Alert[]): { id: string; name: string }[] {
	const groups = new Map<string, string>();
	for (const a of list) groups.set(a.groupId, a.groupName);
	return [...groups]
		.map(([id, name]) => ({ id, name }))
		.sort((x, y) => x.name.localeCompare(y.name));
}

const NBSP = ' ';

/** A value in the unit of its rule (ratios 0..1). */
export function alertValue(unit: string, value: number, locale: Locale): string {
	switch (unit) {
		case 'ratio':
		case 'seconds':
		case 'bytes':
		case 'count':
		case 'per_second':
		case 'bytes_per_second':
			return formatValue(unit, value, locale);
		case 'hours':
			return formatNumber(value, locale) + NBSP + 'h';
		case 'per_minute':
			return formatNumber(value, locale) + NBSP + '/min';
		default:
			return formatNumber(value, locale);
	}
}

const fieldLabels: Record<string, () => string> = {
	ts: () => m.alert_field_ts(),
	user: () => m.alert_field_user(),
	ip: () => m.alert_field_ip(),
	port: () => m.alert_field_port(),
	method: () => m.alert_field_method(),
	target: () => m.alert_field_target(),
	command: () => m.alert_field_command(),
	tty: () => m.alert_field_tty(),
	process: () => m.alert_field_process(),
	pid: () => m.alert_field_pid()
};

/** The label of an event field; its name for fields without a translation. */
export function fieldLabel(name: string): string {
	return fieldLabels[name]?.() ?? name;
}

export function stateName(state: AlertState): string {
	switch (state) {
		case AlertState.CRITICAL:
			return m.alert_state_critical();
		case AlertState.WARNING:
			return m.alert_state_warning();
		case AlertState.PENDING:
			return m.alert_state_pending();
		case AlertState.RESOLVED:
			return m.alert_state_resolved();
		default:
			return m.alert_state_ok();
	}
}
