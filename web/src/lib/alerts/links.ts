/**
 * Jumps from an alert into the node page (M3-19): the charts around its start, in the area of
 * its rule. The node page reads the range and the area from its URL.
 */

/** The charts show an hour before and after the start of an alert, never beyond now. */
export const CHART_WINDOW = 3600_000;

/** The areas (tabs) of the node page a link may open. */
export const nodeTabs = [
	'cpu',
	'memory',
	'disk',
	'network',
	'processes',
	'logs',
	'security',
	'identity'
] as const;
export type NodeTab = (typeof nodeTabs)[number];

/** The area of a rule's category: cpu, memory, disk, network and security have their own. */
export function tabOfCategory(category: string | undefined): NodeTab | undefined {
	return category &&
		(['cpu', 'memory', 'disk', 'network', 'security'] as string[]).includes(category)
		? (category as NodeTab)
		: undefined;
}

/** The query of the node page for the charts around at. */
export function chartsQuery(at: number, now: number, category?: string): string {
	const params = new URLSearchParams({
		from: new Date(at - CHART_WINDOW).toISOString(),
		to: new Date(Math.min(at + CHART_WINDOW, Math.max(now, at + 60_000))).toISOString()
	});
	const tab = tabOfCategory(category);
	if (tab) params.set('tab', tab);
	return `?${params}`;
}

/** What the URL of the node page asks for: a range and an area; invalid values are dropped. */
export function parseNodeView(params: { get(name: string): string | null }): {
	range?: { from: number; to: number };
	tab?: NodeTab;
} {
	const out: { range?: { from: number; to: number }; tab?: NodeTab } = {};
	const from = Date.parse(params.get('from') ?? '');
	const to = Date.parse(params.get('to') ?? '');
	if (Number.isFinite(from) && Number.isFinite(to) && to > from) out.range = { from, to };
	const tab = params.get('tab');
	if (tab && (nodeTabs as readonly string[]).includes(tab)) out.tab = tab as NodeTab;
	return out;
}
