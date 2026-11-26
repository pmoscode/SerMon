/**
 * The variables of a dashboard (ADR 0040): the viewer picks a node or a group, the choice lives in
 * the URL (`?var-<name>=<id>`), not in the document. Only nodes and groups the viewer sees count;
 * otherwise the default applies, else the first one.
 */
import {
	VariableKind,
	WidgetKind,
	type DashboardSpec,
	type Widget
} from '#lib/api/gen/sermon/web/v1/dashboard_pb.js';

/** A node of the fleet as the dashboards need it. */
export interface FleetNode {
	id: string;
	hostname: string;
	groupId: string;
	groupName: string;
}

export type VariableValues = Record<string, string | undefined>;

export function variableParam(name: string): string {
	return `var-${name}`;
}

/** The value of every variable: the URL's choice, the default or the first visible one. */
export function resolveVariables(
	spec: Pick<DashboardSpec, 'variables'>,
	params: Pick<URLSearchParams, 'get'>,
	fleet: FleetNode[]
): VariableValues {
	const nodes = new Set(fleet.map((n) => n.id));
	const groups = [...new Set(fleet.map((n) => n.groupId))].sort((a, b) =>
		groupName(fleet, a).localeCompare(groupName(fleet, b))
	);
	const known = (kind: VariableKind, id: string | null | undefined) =>
		!!id && (kind === VariableKind.GROUP ? groups.includes(id) : nodes.has(id));
	const first = (kind: VariableKind) =>
		kind === VariableKind.GROUP
			? groups[0]
			: [...fleet].sort((a, b) => a.hostname.localeCompare(b.hostname))[0]?.id;
	const out: VariableValues = {};
	for (const v of spec.variables) {
		const chosen = params.get(variableParam(v.name));
		out[v.name] = known(v.kind, chosen)
			? (chosen ?? undefined)
			: known(v.kind, v.defaultId)
				? v.defaultId
				: first(v.kind);
	}
	return out;
}

function groupName(fleet: FleetNode[], id: string): string {
	return fleet.find((n) => n.groupId === id)?.groupName ?? '';
}

/** The nodes a widget reads: fixed ones, those of a group, or all (an alert list without target). */
export interface Target {
	nodeIds: string[];
	groupId?: string;
	all?: boolean;
}

export function targetOf(w: Widget, values: VariableValues, fleet: FleetNode[]): Target {
	const t = w.query?.target;
	const ofGroup = (groupId: string): Target => ({
		nodeIds: fleet.filter((n) => n.groupId === groupId).map((n) => n.id),
		groupId
	});
	if (t?.nodeIds.length) return { nodeIds: [...t.nodeIds] };
	if (t?.groupId) return ofGroup(t.groupId);
	if (t?.variable) {
		const value = values[t.variable];
		if (!value) return { nodeIds: [] };
		return fleet.some((n) => n.groupId === value) && !fleet.some((n) => n.id === value)
			? ofGroup(value)
			: { nodeIds: [value] };
	}
	return w.kind === WidgetKind.ALERT_LIST ? { all: true, nodeIds: [] } : { nodeIds: [] };
}

/** The URL with the choice of a variable (empty removes it). */
export function withVariable(url: { href: string }, name: string, value: string): URL {
	const next = new URL(url.href);
	if (value) next.searchParams.set(variableParam(name), value);
	else next.searchParams.delete(variableParam(name));
	return next;
}
