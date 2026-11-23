/**
 * The form of a group's log settings (M3-12): what the browser can check before the server and
 * the agent check again. Globs know * and ? only (M3-05); regexes use the RE2/Rust syntax, which
 * the browser can't judge – the server names a rule it can't compile.
 */
import type { MessageInitShape } from '@bufbuild/protobuf';
import {
	LogSeverity,
	type LogSettings,
	type LogSettingsSchema
} from '#lib/api/gen/sermon/web/v1/log_settings_pb.js';
import { severities, type SeverityName } from '#lib/logs/filter.js';

/** Bounds of the protocol (log_settings.proto). */
export const MAX_FILES = 16;
export const MAX_UNITS = 64;
export const MAX_RULES = 32;
const MAX_GLOB_BYTES = 1024;
const MAX_REGEX_BYTES = 1024;
const UNIT = /^[A-Za-z0-9@:._\\-]{1,256}$/;

export interface GlobDraft {
	pattern: string;
	/** The regex of an entry's first line; empty: by indentation. */
	multiline: string;
}

export interface GroupDraft {
	enabled: boolean;
	minSeverity: SeverityName | '';
	/** Excluded units, one per line or separated by commas. */
	excludeUnits: string;
	files: GlobDraft[];
}

export type GlobProblem = 'empty' | 'absolute' | 'segments' | 'wildcards' | 'long';
export type FileProblem = GlobProblem | 'duplicate' | 'multiline';

const bytes = (text: string) => new TextEncoder().encode(text).length;

/** What is wrong with a pattern, if anything. */
export function globProblem(pattern: string): GlobProblem | undefined {
	if (pattern === '') return 'empty';
	if (!pattern.startsWith('/') || pattern.includes('\u0000')) return 'absolute';
	if (bytes(pattern) > MAX_GLOB_BYTES) return 'long';
	if (/[[\]\\]/.test(pattern)) return 'wildcards';
	if (
		pattern
			.slice(1)
			.split('/')
			.some((s) => s === '' || s === '.' || s === '..')
	)
		return 'segments';
	return undefined;
}

/** The units of the text field. */
export function unitsOf(text: string): { units: string[]; problem?: 'unit' | 'units' } {
	const units = [
		...new Set(
			text
				.split(/[,\n]/)
				.map((u) => u.trim())
				.filter(Boolean)
		)
	];
	if (units.some((u) => !UNIT.test(u))) return { units, problem: 'unit' };
	if (units.length > MAX_UNITS) return { units, problem: 'units' };
	return { units };
}

/** What the browser can say about a redaction rule. */
export function ruleProblem(pattern: string): 'empty' | 'long' | undefined {
	if (pattern === '') return 'empty';
	if (bytes(pattern) > MAX_REGEX_BYTES) return 'long';
	return undefined;
}

export interface Problems {
	units?: 'unit' | 'units';
	/** Per file row; undefined: fine. */
	files: (FileProblem | undefined)[];
	tooManyFiles?: true;
}

/** Whether the problems allow saving. */
export function valid(p: Problems): boolean {
	return !p.units && !p.tooManyFiles && p.files.every((f) => f === undefined);
}

/** The problems of a draft; empty file rows are no problem (they are dropped). */
export function problems(draft: GroupDraft): Problems {
	const seen = new Set<string>();
	const files = draft.files.map((f): FileProblem | undefined => {
		const pattern = f.pattern.trim();
		if (pattern === '' && f.multiline.trim() === '') return undefined;
		const glob = globProblem(pattern);
		if (glob) return glob;
		if (seen.has(pattern)) return 'duplicate';
		seen.add(pattern);
		if (bytes(f.multiline) > MAX_REGEX_BYTES) return 'multiline';
		return undefined;
	});
	const out: Problems = { files };
	const units = unitsOf(draft.excludeUnits).problem;
	if (units) out.units = units;
	if (draft.files.filter((f) => f.pattern.trim() !== '').length > MAX_FILES)
		out.tooManyFiles = true;
	return out;
}

/** The form of stored settings. */
export function draftOf(settings: LogSettings | undefined): GroupDraft {
	const code = settings?.minSeverity ?? LogSeverity.UNSPECIFIED;
	return {
		enabled: settings?.enabled ?? true,
		minSeverity: code >= 1 && code <= severities.length ? severities[code - 1] : '',
		excludeUnits: (settings?.excludeUnits ?? []).join('\n'),
		files: (settings?.files ?? []).map((f) => ({ pattern: f.pattern, multiline: f.multilineStart }))
	};
}

/** The settings of a form. */
export function settingsOf(draft: GroupDraft): MessageInitShape<typeof LogSettingsSchema> {
	return {
		enabled: draft.enabled,
		minSeverity: draft.minSeverity
			? ((severities.indexOf(draft.minSeverity) + 1) as LogSeverity)
			: LogSeverity.UNSPECIFIED,
		excludeUnits: unitsOf(draft.excludeUnits).units,
		files: draft.files
			.map((f) => ({ pattern: f.pattern.trim(), multilineStart: f.multiline.trim() }))
			.filter((f) => f.pattern !== '')
	};
}
