import { timestampMs } from '@bufbuild/protobuf/wkt';
import type { LogLine } from '#lib/api/gen/sermon/web/v1/log_pb.js';

/** The list keeps at most this many lines; the oldest go first. */
export const MAX_LINES = 2000;

/** Identifies a line: its stream, time and text. */
export function lineKey(l: LogLine): string {
	return `${l.nodeId}|${l.source}|${l.unit}|${l.at?.seconds ?? 0}.${l.at?.nanos ?? 0}|${l.message}`;
}

/** The time of a line in ms. */
export function lineMs(l: LogLine): number {
	return l.at ? timestampMs(l.at) : 0;
}

function compareNewestFirst(a: LogLine, b: LogLine): number {
	const ds = Number((b.at?.seconds ?? 0n) - (a.at?.seconds ?? 0n));
	return ds !== 0 ? ds : (b.at?.nanos ?? 0) - (a.at?.nanos ?? 0);
}

/** The lines of both lists without duplicates, newest first, at most MAX_LINES. */
export function merge(lines: LogLine[], incoming: LogLine[]): LogLine[] {
	const seen = new Set(lines.map(lineKey));
	const fresh = incoming.filter((l) => {
		const key = lineKey(l);
		if (seen.has(key)) return false;
		seen.add(key);
		return true;
	});
	if (fresh.length === 0) return lines;
	return [...fresh, ...lines].sort(compareNewestFirst).slice(0, MAX_LINES);
}
