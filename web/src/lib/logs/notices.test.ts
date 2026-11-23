import { describe, expect, it } from 'vitest';
import { lostInRange, selectedNodes } from './notices.js';
import { emptyFilter } from './filter.js';

const n = (nodeId: string, groupId: string, logsVetoed = false) => ({
	nodeId,
	groupId,
	hostname: nodeId,
	logsVetoed
});

describe('lostInRange', () => {
	it('counts what the counter rose within the range, also across a restart', () => {
		const series = (values: number[]) => values.map((max) => ({ max }));
		expect(lostInRange(series([0, 0, 0]))).toBe(0);
		// Already lost before the range: no rise, nothing in it.
		expect(lostInRange(series([5, 5, 5]))).toBe(0);
		expect(lostInRange(series([5, 7, 12]))).toBe(7);
		// A restart resets the counter; its new value is the gap since the last run.
		expect(lostInRange(series([12, 3, 4]))).toBe(4);
		expect(lostInRange([])).toBe(0);
	});
});

describe('selectedNodes', () => {
	const nodes = [n('a', 'web'), n('b', 'web', true), n('c', 'db', true)];
	it('is the fixed node, the chosen nodes, the group or all', () => {
		expect(selectedNodes(nodes, emptyFilter(), 'c').map((x) => x.nodeId)).toEqual(['c']);
		expect(
			selectedNodes(nodes, { ...emptyFilter(), nodes: ['a', 'c'] }).map((x) => x.nodeId)
		).toEqual(['a', 'c']);
		expect(selectedNodes(nodes, { ...emptyFilter(), group: 'web' }).map((x) => x.nodeId)).toEqual([
			'a',
			'b'
		]);
		expect(selectedNodes(nodes, { ...emptyFilter(), group: 'web', nodes: ['c'] })).toEqual([]);
		expect(selectedNodes(nodes, emptyFilter())).toHaveLength(3);
	});
});
