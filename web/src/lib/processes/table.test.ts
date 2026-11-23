import { create, type MessageInitShape } from '@bufbuild/protobuf';
import { describe, expect, it } from 'vitest';
import { ProcessInfoSchema, type ProcessInfo } from '#lib/api/gen/sermon/web/v1/live_view_pb.js';
import {
	compileRegex,
	DEFAULT_SORT,
	nextSort,
	processRows,
	users,
	type ProcessFilter,
	type Sort
} from './table.js';

function proc(pid: number, ppid: number, extra: MessageInitShape<typeof ProcessInfoSchema> = {}) {
	return create(ProcessInfoSchema, {
		pid,
		ppid,
		name: `p${pid}`,
		user: 'root',
		state: 'S',
		threads: 1,
		...extra
	});
}

const none: ProcessFilter = { user: '', name: '', regex: '' };
const byPid: Sort = { key: 'pid', desc: false };
const pids = (rows: { process: ProcessInfo }[]) => rows.map((r) => r.process.pid);

function rows(list: ProcessInfo[], sort: Sort = byPid, filter = none, tree = false) {
	return processRows(list, { sort, filter, regex: compileRegex(filter.regex).regex, tree });
}

describe('sorting', () => {
	const list = [
		proc(10, 1, { cpuRatio: 0.2, rssBytes: 300n, memoryRatio: 0.3, name: 'nginx' }),
		proc(11, 1, { name: 'bash' }), // first snapshot: no CPU yet, memory not readable
		proc(12, 1, { cpuRatio: 1.5, rssBytes: 100n, memoryRatio: 0.1, name: 'Postgres' }),
		proc(13, 1, { cpuRatio: 0.2, rssBytes: 200n, memoryRatio: 0.2, name: 'cron' })
	];

	it('sorts by CPU, busiest first, ties by PID and unreadable values last', () => {
		expect(pids(rows(list, { key: 'cpu', desc: true }))).toEqual([12, 10, 13, 11]);
	});

	it('keeps unreadable values last in both directions', () => {
		expect(pids(rows(list, { key: 'cpu', desc: false }))).toEqual([10, 13, 12, 11]);
		expect(pids(rows(list, { key: 'rss', desc: true }))).toEqual([10, 13, 12, 11]);
		expect(pids(rows(list, { key: 'rss', desc: false }))).toEqual([12, 13, 10, 11]);
	});

	it('sorts names without regard to case', () => {
		expect(pids(rows(list, { key: 'name', desc: false }))).toEqual([11, 13, 10, 12]);
		expect(pids(rows(list, { key: 'name', desc: true }))).toEqual([12, 10, 13, 11]);
	});

	it('sorts by PID descending', () => {
		expect(pids(rows(list, { key: 'pid', desc: true }))).toEqual([13, 12, 11, 10]);
	});

	it('turns a column around and starts a new one with its usual direction', () => {
		expect(nextSort(DEFAULT_SORT, 'cpu')).toEqual({ key: 'cpu', desc: false });
		expect(nextSort(DEFAULT_SORT, 'rss')).toEqual({ key: 'rss', desc: true });
		expect(nextSort(DEFAULT_SORT, 'name')).toEqual({ key: 'name', desc: false });
		expect(nextSort({ key: 'name', desc: false }, 'name')).toEqual({ key: 'name', desc: true });
		expect(nextSort(DEFAULT_SORT, 'pid')).toEqual({ key: 'pid', desc: false });
	});

	it('shows a PID only once', () => {
		const twice = [proc(5, 1), proc(5, 1, { name: 'again' }), proc(6, 1)];
		expect(pids(rows(twice))).toEqual([5, 6]);
		expect(rows(twice)[0].process.name).toBe('p5');
	});
});

describe('tree', () => {
	it('nests children under their parent, siblings in the sort order', () => {
		const list = [
			proc(1, 0, { name: 'systemd' }),
			proc(20, 1, { cpuRatio: 0.1 }),
			proc(21, 20, { cpuRatio: 0.5 }),
			proc(30, 1, { cpuRatio: 0.9 }),
			proc(22, 20, { cpuRatio: 0.7 })
		];
		const tree = rows(list, { key: 'cpu', desc: true }, none, true);
		expect(tree.map((r) => [r.process.pid, r.depth])).toEqual([
			[1, 0],
			[30, 1],
			[20, 1],
			[22, 2],
			[21, 2]
		]);
	});

	it('shows processes with a missing parent as roots', () => {
		const list = [proc(2, 0), proc(50, 49), proc(51, 50), proc(60, 2)];
		const tree = rows(list, byPid, none, true);
		expect(tree.map((r) => [r.process.pid, r.depth])).toEqual([
			[2, 0],
			[60, 1],
			[50, 0],
			[51, 1]
		]);
	});

	it('breaks a cycle of parents at its lowest PID and shows every process once', () => {
		// 7 → 8 → 9 → 7, 10 hangs below 9, 4 is its own parent.
		const list = [proc(8, 7), proc(9, 8), proc(7, 9), proc(10, 9), proc(4, 4)];
		const tree = rows(list, byPid, none, true);
		expect(tree.map((r) => [r.process.pid, r.depth])).toEqual([
			[4, 0],
			[7, 0],
			[8, 1],
			[9, 2],
			[10, 3]
		]);
	});

	it('handles a deep chain without recursion limits', () => {
		const list = Array.from({ length: 4000 }, (_, i) => proc(i + 1, i));
		const tree = rows(list, byPid, none, true);
		expect(tree).toHaveLength(4000);
		expect(tree.at(-1)?.depth).toBe(3999);
	});

	it('keeps the parents of a match as context', () => {
		const list = [
			proc(1, 0, { name: 'systemd' }),
			proc(20, 1, { name: 'sshd' }),
			proc(21, 20, { name: 'bash' }),
			proc(30, 1, { name: 'cron' })
		];
		const tree = rows(list, byPid, { ...none, name: 'bash' }, true);
		expect(tree.map((r) => [r.process.pid, r.depth, r.context])).toEqual([
			[1, 0, true],
			[20, 1, true],
			[21, 2, false]
		]);
	});
});

describe('filters', () => {
	const list = [
		proc(1, 0, { name: 'systemd', user: 'root', cmdline: '/sbin/init splash' }),
		proc(2, 1, { name: 'postgres', user: 'postgres', cmdline: 'postgres: checkpointer' }),
		proc(3, 1, { name: 'nginx', user: 'www-data', cmdline: 'nginx: worker process' }),
		proc(4, 2, { name: 'kworker/0:1', user: 'root', kernelThread: true, cmdline: '' }),
		proc(5, 1, { name: 'NGINX-helper', user: '' })
	];

	it('filters by user', () => {
		expect(pids(rows(list, byPid, { ...none, user: 'root' }))).toEqual([1, 4]);
	});

	it('filters by a part of the name, without regard to case', () => {
		expect(pids(rows(list, byPid, { ...none, name: 'nginx' }))).toEqual([3, 5]);
		expect(pids(rows(list, byPid, { ...none, name: '  ' }))).toEqual([1, 2, 3, 4, 5]);
	});

	it('filters by a regular expression over name and command line', () => {
		expect(pids(rows(list, byPid, { ...none, regex: '^post' }))).toEqual([2]);
		expect(pids(rows(list, byPid, { ...none, regex: 'worker\\s+process$' }))).toEqual([3]);
		expect(pids(rows(list, byPid, { ...none, regex: 'kworker/\\d' }))).toEqual([4]);
	});

	it('combines the filters', () => {
		expect(pids(rows(list, byPid, { user: 'root', name: 'sys', regex: 'init' }))).toEqual([1]);
	});

	it('reports an invalid regular expression and leaves it out', () => {
		const bad = compileRegex('nginx(');
		expect(bad.regex).toBeUndefined();
		expect(bad.error).toBe('invalid');
		expect(bad.detail).toBeTruthy();
		expect(pids(rows(list, byPid, { ...none, regex: 'nginx(' }))).toEqual([1, 2, 3, 4, 5]);
		expect(compileRegex('')).toEqual({});
		expect(compileRegex('x'.repeat(201)).error).toBe('too_long');
		expect(compileRegex('x'.repeat(200)).regex).toBeDefined();
	});

	it('lists the users of a snapshot once, sorted, without empty names', () => {
		expect(users(list)).toEqual(['postgres', 'root', 'www-data']);
	});
});
