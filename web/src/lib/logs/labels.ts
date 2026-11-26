import { LogSource } from '#lib/api/gen/sermon/web/v1/log_pb.js';
import { LogSeverity } from '#lib/api/gen/sermon/web/v1/log_settings_pb.js';
import { m } from '#lib/paraglide/messages.js';
import type { Group } from './histogram.js';
import type { LogPreset, SeverityName } from './filter.js';

/** The name of a severity. */
export function severityLabel(severity: LogSeverity): string {
	switch (severity) {
		case LogSeverity.EMERGENCY:
			return m.logs_severity_emergency();
		case LogSeverity.ALERT:
			return m.logs_severity_alert();
		case LogSeverity.CRITICAL:
			return m.logs_severity_critical();
		case LogSeverity.ERROR:
			return m.logs_severity_error();
		case LogSeverity.WARNING:
			return m.logs_severity_warning();
		case LogSeverity.NOTICE:
			return m.logs_severity_notice();
		case LogSeverity.INFO:
			return m.logs_severity_info();
		case LogSeverity.DEBUG:
			return m.logs_severity_debug();
		default:
			return m.logs_severity_unknown();
	}
}

/** The name of a severity of the filter. */
export function severityNameLabel(name: SeverityName): string {
	const codes: Record<SeverityName, LogSeverity> = {
		emergency: LogSeverity.EMERGENCY,
		alert: LogSeverity.ALERT,
		critical: LogSeverity.CRITICAL,
		error: LogSeverity.ERROR,
		warning: LogSeverity.WARNING,
		notice: LogSeverity.NOTICE,
		info: LogSeverity.INFO,
		debug: LogSeverity.DEBUG
	};
	return severityLabel(codes[name]);
}

/** The name of a group of the histogram. */
export function groupLabel(group: Group): string {
	switch (group) {
		case 'error':
			return m.logs_group_error();
		case 'warning':
			return m.logs_group_warning();
		case 'info':
			return m.logs_group_info();
		default:
			return m.logs_group_debug();
	}
}

export function sourceLabel(source: LogSource | 'journal' | 'file' | 'container'): string {
	if (source === LogSource.FILE || source === 'file') return m.logs_source_file();
	if (source === LogSource.CONTAINER || source === 'container') return m.logs_source_container();
	return m.logs_source_journal();
}

export const presetLabels: Record<LogPreset, () => string> = {
	'15m': () => m.range_15m(),
	'1h': () => m.range_1h(),
	'6h': () => m.range_6h(),
	'24h': () => m.range_24h(),
	'7d': () => m.range_7d(),
	'14d': () => m.range_14d()
};
