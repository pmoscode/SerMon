/**
 * The link from a suggestion ("nginx detected – enable the module?") to the module's form in the
 * administration, with the agent's suggestion to start with.
 */

const ID = /^[a-z][a-z0-9_]{0,31}$/;
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const MAX_SUGGESTION = 1024;

/** The query of the administration's modules tab opening a group's module. */
export function enableQuery(group: string, module: string, suggestion = ''): string {
	const q = new URLSearchParams({ tab: 'modules', group, module });
	if (suggestion) q.set('suggest', suggestion);
	return q.toString();
}

/** What the query asks to open; nothing for anything else (the link may be edited). */
export function openingOf(
	params: Pick<URLSearchParams, 'get'>
): { group: string; module: string; suggestion?: string } | undefined {
	const group = params.get('group') ?? '';
	const module = params.get('module') ?? '';
	if (params.get('tab') !== 'modules' || !UUID.test(group) || !ID.test(module)) return undefined;
	const suggestion = params.get('suggest') ?? '';
	return { group, module, suggestion: suggestion.length <= MAX_SUGGESTION ? suggestion : '' };
}
