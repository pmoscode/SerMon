import type { LogFilter } from './filter.js';

interface NodeLike {
	nodeId: string;
	groupId: string;
}

/** The visible nodes a filter selects. */
export function selectedNodes<T extends NodeLike>(
	nodes: T[],
	filter: LogFilter,
	fixedNode?: string
): T[] {
	if (fixedNode) return nodes.filter((n) => n.nodeId === fixedNode);
	return nodes.filter(
		(n) =>
			(filter.nodes.length === 0 || filter.nodes.includes(n.nodeId)) &&
			(!filter.group || n.groupId === filter.group)
	);
}

/**
 * How many journal entries the agent lost within a range, from the points of
 * sermon_agent_journal_lost_entries (a count since the agent's start, M2-05). A drop is a restart:
 * the new value is the gap since the previous run.
 */
export function lostInRange(points: { max: number }[]): number {
	let lost = 0;
	for (let i = 1; i < points.length; i++) {
		const delta = points[i].max - points[i - 1].max;
		lost += delta >= 0 ? delta : points[i].max;
	}
	return lost;
}
