import type { KeyFigures, NodeSummary } from '#lib/api/gen/sermon/web/v1/fleet_pb.js';

/**
 * Traffic light of a node. Until the alert engine exists (M2), it comes from the key figures:
 * warning from 80 %, critical from 90 % CPU, memory or the fullest file system.
 */
export type Status = 'ok' | 'warning' | 'critical' | 'offline';

export const WARNING = 0.8;
export const CRITICAL = 0.9;

/** Which figure caused the status. */
export type Reason = 'cpu' | 'memory' | 'filesystem';

export interface Assessment {
	status: Status;
	reasons: Reason[];
}

/** The status of a node (summary or details) and the figures above the thresholds (highest first). */
export function assess(node: { online: boolean; figures?: KeyFigures }): Assessment {
	if (!node.online) return { status: 'offline', reasons: [] };
	const figures: [Reason, number | undefined][] = [
		['cpu', node.figures?.cpuBusyRatio],
		['memory', node.figures?.memoryUsedRatio],
		['filesystem', node.figures?.filesystemMaxUsedRatio]
	];
	const above = figures
		.filter((f): f is [Reason, number] => f[1] !== undefined && f[1] >= WARNING)
		.sort((a, b) => b[1] - a[1]);
	if (above.length === 0) return { status: 'ok', reasons: [] };
	return {
		status: above[0][1] >= CRITICAL ? 'critical' : 'warning',
		reasons: above.map(([r]) => r)
	};
}

/** Order for sorting: problems first. */
export const severity: Record<Status, number> = { critical: 0, warning: 1, offline: 2, ok: 3 };

/** Counts per status for the header. */
export function countByStatus(nodes: NodeSummary[]): Record<Status, number> {
	const counts: Record<Status, number> = { ok: 0, warning: 0, critical: 0, offline: 0 };
	for (const node of nodes) counts[assess(node).status]++;
	return counts;
}

export interface FleetFilter {
	search: string;
	group: string; // group ID, empty: all
	status: Status | '';
}

/** Nodes matching the filter, problems first, then by host name. */
export function filterNodes(nodes: NodeSummary[], filter: FleetFilter): NodeSummary[] {
	const search = filter.search.trim().toLowerCase();
	return nodes
		.filter((n) => !filter.group || n.groupId === filter.group)
		.filter((n) => !filter.status || assess(n).status === filter.status)
		.filter(
			(n) =>
				!search ||
				n.hostname.toLowerCase().includes(search) ||
				n.groupName.toLowerCase().includes(search) ||
				`${n.osId} ${n.osVersion}`.toLowerCase().includes(search)
		)
		.sort(
			(a, b) =>
				severity[assess(a).status] - severity[assess(b).status] ||
				a.hostname.localeCompare(b.hostname)
		);
}

/** The groups that occur, sorted by name. */
export function groupsOf(nodes: NodeSummary[]): { id: string; name: string }[] {
	const groups = new Map(nodes.map((n) => [n.groupId, n.groupName]));
	return [...groups]
		.map(([id, name]) => ({ id, name }))
		.sort((a, b) => a.name.localeCompare(b.name));
}
