/**
 * Free tags of nodes (M2-29, KONZEPT §7): key=value pairs such as env=prod for filters and for
 * matching in rules, notification policies and silences. The rules are the server's
 * (domain/tag); the server checks again.
 */

export const MAX_TAGS = 16;
const KEY = /^[a-z][a-z0-9_-]{0,31}$/;
const VALUE = /^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,62}$/;

/** Tags from "key=value" pairs separated by commas, spaces or lines; error is the first bad pair. */
export function parseTags(text: string): { tags: Record<string, string> } | { error: string } {
	const tags: Record<string, string> = {};
	for (const pair of text.split(/[\s,]+/).filter(Boolean)) {
		const at = pair.indexOf('=');
		const key = at < 0 ? '' : pair.slice(0, at);
		const value = at < 0 ? '' : pair.slice(at + 1);
		if (
			!KEY.test(key) ||
			!VALUE.test(value) ||
			key in tags ||
			Object.keys(tags).length >= MAX_TAGS
		) {
			return { error: pair };
		}
		tags[key] = value;
	}
	return { tags };
}

/** The pairs sorted by key, e.g. ["env=prod", "role=db"]. */
export function tagPairs(tags: Record<string, string>): string[] {
	return Object.keys(tags)
		.sort()
		.map((k) => `${k}=${tags[k]}`);
}

/** The pairs for a text field, e.g. "env=prod, role=db". */
export function tagsText(tags: Record<string, string>): string {
	return tagPairs(tags).join(', ');
}
