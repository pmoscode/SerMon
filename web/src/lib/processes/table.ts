/**
 * The rows of the live process view (KONZEPT §9, M3-15): sorted, filtered in the browser and
 * optionally as a tree from the PPIDs. Pure, so that 4,000 processes every 2 s stay cheap to test.
 */
import type { ProcessInfo } from '#lib/api/gen/sermon/web/v1/live_view_pb.js';

export type SortKey =
	| 'pid'
	| 'ppid'
	| 'user'
	| 'state'
	| 'cpu'
	| 'memory'
	| 'rss'
	| 'threads'
	| 'runtime'
	| 'name'
	| 'command';

export interface Sort {
	key: SortKey;
	desc: boolean;
}

/** Like top: the busiest processes first. */
export const DEFAULT_SORT: Sort = { key: 'cpu', desc: true };

const NUMERIC: SortKey[] = ['cpu', 'memory', 'rss', 'threads', 'runtime'];

/** The sort after a click on a column: the same column turns around, a new one starts with its usual direction. */
export function nextSort(current: Sort, key: SortKey): Sort {
	if (current.key === key) return { key, desc: !current.desc };
	return { key, desc: NUMERIC.includes(key) };
}

export interface ProcessFilter {
	/** The exact user name; empty: everyone. */
	user: string;
	/** A part of the process name, without regard to case. */
	name: string;
	/** A regular expression over the name and the command line. */
	regex: string;
}

export interface ProcessRow {
	process: ProcessInfo;
	/** The level in the tree; 0 outside the tree. */
	depth: number;
	/** In the tree: shown only as the parent of a match. */
	context: boolean;
}

/** Longer expressions are refused: the browser runs them over every command line. */
export const MAX_REGEX = 200;

/** The regular expression of the filter; error names why it is invalid (it filters nothing then). */
export function compileRegex(source: string): {
	regex?: RegExp;
	error?: 'too_long' | 'invalid';
	detail?: string;
} {
	if (source === '') return {};
	if (source.length > MAX_REGEX) return { error: 'too_long' };
	try {
		return { regex: new RegExp(source, 'u') };
	} catch (error) {
		return { error: 'invalid', detail: error instanceof Error ? error.message : String(error) };
	}
}

/** The user names of a snapshot, once each, sorted. */
export function users(processes: ProcessInfo[]): string[] {
	return [...new Set(processes.map((p) => p.user).filter((u) => u !== ''))].sort((a, b) =>
		a.localeCompare(b)
	);
}

type Value = number | string | undefined;

function value(p: ProcessInfo, key: SortKey): Value {
	switch (key) {
		case 'pid':
			return p.pid;
		case 'ppid':
			return p.ppid;
		case 'user':
			return p.user || undefined;
		case 'state':
			return p.state || undefined;
		case 'cpu':
			return p.cpuRatio;
		case 'memory':
			return p.memoryRatio;
		case 'rss':
			return p.rssBytes === undefined ? undefined : Number(p.rssBytes);
		case 'threads':
			return p.threads;
		case 'runtime':
			return p.runtimeSeconds === undefined ? undefined : Number(p.runtimeSeconds);
		case 'name':
			return p.name;
		case 'command':
			return p.cmdline || undefined;
	}
}

const collator = new Intl.Collator(undefined, { sensitivity: 'base', numeric: true });

/** Compares by the sort key; unreadable values ("n/a") last in both directions, then by PID. */
export function comparator(sort: Sort): (a: ProcessInfo, b: ProcessInfo) => number {
	return (a, b) => {
		const x = value(a, sort.key);
		const y = value(b, sort.key);
		if (x === undefined || y === undefined) {
			if (x !== y) return x === undefined ? 1 : -1;
		} else {
			const order =
				typeof x === 'number' && typeof y === 'number'
					? x - y
					: collator.compare(String(x), String(y));
			if (order !== 0) return sort.desc ? -order : order;
		}
		return a.pid - b.pid;
	};
}

function matcher(filter: ProcessFilter, regex: RegExp | undefined) {
	const name = filter.name.trim().toLowerCase();
	return (p: ProcessInfo) =>
		(filter.user === '' || p.user === filter.user) &&
		(name === '' || p.name.toLowerCase().includes(name)) &&
		(!regex || regex.test(p.name) || (p.cmdline !== undefined && regex.test(p.cmdline)));
}

/** The parent of each process that has one in the list; cycles are broken at their lowest PID. */
function parents(byPid: Map<number, ProcessInfo>): Map<number, number> {
	const parent = new Map<number, number>();
	for (const p of byPid.values())
		if (p.ppid !== p.pid && byPid.has(p.ppid)) parent.set(p.pid, p.ppid);
	const state = new Map<number, 'path' | 'done'>();
	for (const pid of byPid.keys()) {
		const path: number[] = [];
		let current: number | undefined = pid;
		while (current !== undefined && !state.has(current)) {
			state.set(current, 'path');
			path.push(current);
			current = parent.get(current);
		}
		if (current !== undefined && state.get(current) === 'path') {
			const cycle = path.slice(path.indexOf(current));
			parent.delete(Math.min(...cycle));
		}
		for (const p of path) state.set(p, 'done');
	}
	return parent;
}

function tree(
	byPid: Map<number, ProcessInfo>,
	compare: (a: ProcessInfo, b: ProcessInfo) => number,
	matches: (p: ProcessInfo) => boolean
): ProcessRow[] {
	const parent = parents(byPid);
	// Matches and their ancestors are shown; the ancestors only as context.
	const shown = new Map<number, boolean>();
	for (const p of byPid.values()) {
		if (!matches(p)) continue;
		shown.set(p.pid, false);
		for (let up = parent.get(p.pid); up !== undefined && !shown.has(up); up = parent.get(up))
			shown.set(up, true);
	}
	const children = new Map<number, ProcessInfo[]>();
	const roots: ProcessInfo[] = [];
	for (const p of byPid.values()) {
		if (!shown.has(p.pid)) continue;
		const up = parent.get(p.pid);
		if (up === undefined) roots.push(p);
		else {
			const siblings = children.get(up);
			if (siblings) siblings.push(p);
			else children.set(up, [p]);
		}
	}
	const rows: ProcessRow[] = [];
	const stack = roots
		.sort(compare)
		.reverse()
		.map((process) => ({ process, depth: 0 }));
	while (stack.length > 0) {
		const { process, depth } = stack.pop()!;
		rows.push({ process, depth, context: shown.get(process.pid)! });
		const below = (children.get(process.pid) ?? []).sort(compare);
		for (let i = below.length - 1; i >= 0; i--) stack.push({ process: below[i], depth: depth + 1 });
	}
	return rows;
}

/** The rows to show: filtered and sorted, as a list or as a tree. */
export function processRows(
	processes: ProcessInfo[],
	options: { sort: Sort; filter: ProcessFilter; regex: RegExp | undefined; tree: boolean }
): ProcessRow[] {
	const byPid = new Map<number, ProcessInfo>();
	for (const p of processes) if (!byPid.has(p.pid)) byPid.set(p.pid, p);
	const compare = comparator(options.sort);
	const matches = matcher(options.filter, options.regex);
	if (options.tree) return tree(byPid, compare, matches);
	return [...byPid.values()]
		.filter(matches)
		.sort(compare)
		.map((process) => ({ process, depth: 0, context: false }));
}
