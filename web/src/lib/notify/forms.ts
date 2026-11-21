/** Pure helpers of the notification forms (M2-18): patterns, labels, times, secrets. */
import { Weekday } from '#lib/api/gen/sermon/web/v1/silence_pb.js';
import { m } from '#lib/paraglide/messages.js';

/** Rule patterns from a text field: separated by commas, spaces or lines, each once. */
export function parsePatterns(text: string): string[] {
	return [...new Set(text.split(/[\s,]+/).filter(Boolean))];
}

export function patternsText(patterns: string[]): string {
	return patterns.join(', ');
}

/** Labels from "key=value" lines; error is the number of the first bad line. */
export function parseLabels(text: string): { labels: Record<string, string> } | { error: number } {
	const labels: Record<string, string> = {};
	const lines = text.split('\n');
	for (let i = 0; i < lines.length; i++) {
		const line = lines[i].trim();
		if (!line) continue;
		const at = line.indexOf('=');
		const key = at < 0 ? '' : line.slice(0, at).trim();
		const value = at < 0 ? '' : line.slice(at + 1).trim();
		if (!key || !value) return { error: i + 1 };
		labels[key] = value;
	}
	return { labels };
}

export function labelsText(labels: Record<string, string>): string {
	return Object.keys(labels)
		.sort()
		.map((k) => `${k}=${labels[k]}`)
		.join('\n');
}

/** Minutes after midnight of an <input type="time"> value. */
export function minutesOfTime(value: string): number | undefined {
	const match = /^(\d{2}):(\d{2})$/.exec(value);
	return match ? Number(match[1]) * 60 + Number(match[2]) : undefined;
}

export function timeOfMinutes(minutes: number): string {
	const pad = (n: number) => String(n).padStart(2, '0');
	return `${pad(Math.floor(minutes / 60))}:${pad(minutes % 60)}`;
}

/** A duration in minutes as "2 h 30 min" (both languages write h and min). */
export function durationText(minutes: number): string {
	const h = Math.floor(minutes / 60);
	const min = minutes % 60;
	return [h ? `${h} h` : '', min ? `${min} min` : ''].filter(Boolean).join(' ') || '0 min';
}

/** The weekdays from Monday, with their names. */
export const weekdays = [
	{ day: Weekday.MONDAY, name: () => m.weekday_monday() },
	{ day: Weekday.TUESDAY, name: () => m.weekday_tuesday() },
	{ day: Weekday.WEDNESDAY, name: () => m.weekday_wednesday() },
	{ day: Weekday.THURSDAY, name: () => m.weekday_thursday() },
	{ day: Weekday.FRIDAY, name: () => m.weekday_friday() },
	{ day: Weekday.SATURDAY, name: () => m.weekday_saturday() },
	{ day: Weekday.SUNDAY, name: () => m.weekday_sunday() }
];

/** The typed secrets only: an empty field keeps the stored secret (they are write-only). */
export function secretUpdate(values: Record<string, string>): Record<string, string> {
	return Object.fromEntries(Object.entries(values).filter(([, v]) => v !== ''));
}

/** Whether a list of group IDs from the session ("*" for all) covers the groups. */
export function coversGroups(allowed: string[], groups: string[]): boolean {
	if (allowed.includes('*')) return true;
	return groups.length > 0 && groups.every((g) => allowed.includes(g));
}

/** Durations offered for a silence, in minutes. */
export const silenceDurations = [60, 240, 480, 1440, 4320, 10080];
