import { describe, expect, it } from 'vitest';
import {
	addNode,
	combine,
	compareParams,
	MAX_NODES,
	parseCompare,
	removeNode,
	sharedMax,
	type CompareSelection
} from './compare.js';

const a = '0193f5a0-0000-7000-8000-000000000001';
const b = '0193f5a0-0000-7000-8000-000000000002';
const c = '0193f5a0-0000-7000-8000-000000000003';
const d = '0193f5a0-0000-7000-8000-000000000004';
const e = '0193f5a0-0000-7000-8000-000000000005';

const params = (query: string) => new URLSearchParams(query);

describe('parseCompare', () => {
	it('reads the nodes and the view from the URL', () => {
		expect(parseCompare(params(`node=${a}&node=${b}&view=overlay`))).toEqual({
			nodes: [a, b],
			view: 'overlay'
		});
	});

	it('shows side by side unless overlay is asked for', () => {
		expect(parseCompare(params(`node=${a}`)).view).toBe('side');
		expect(parseCompare(params(`node=${a}&view=bogus`)).view).toBe('side');
	});

	it('drops what is no node ID and duplicates', () => {
		expect(parseCompare(params(`node=${a}&node=x&node=${a}&node=../${b}&node=${b}`)).nodes).toEqual(
			[a, b]
		);
	});

	it('keeps at most four nodes', () => {
		const query = [a, b, c, d, e].map((id) => `node=${id}`).join('&');
		expect(parseCompare(params(query)).nodes).toEqual([a, b, c, d]);
		expect(MAX_NODES).toBe(4);
	});

	it('round-trips through compareParams', () => {
		const selection = { nodes: [b, a], view: 'overlay' as const };
		expect(parseCompare(compareParams(selection))).toEqual(selection);
		expect(compareParams({ nodes: [a], view: 'side' }).toString()).toBe(`node=${a}`);
	});
});

describe('addNode and removeNode', () => {
	it('adds up to four nodes, once each', () => {
		let s: CompareSelection = { nodes: [a], view: 'side' };
		s = addNode(s, b);
		s = addNode(s, b);
		s = addNode(s, c);
		s = addNode(s, d);
		expect(s.nodes).toEqual([a, b, c, d]);
		expect(addNode(s, e).nodes).toEqual([a, b, c, d]);
	});

	it('ignores what is no node ID', () => {
		expect(addNode({ nodes: [], view: 'side' }, 'nope').nodes).toEqual([]);
	});

	it('removes a node and keeps the view', () => {
		expect(removeNode({ nodes: [a, b, c], view: 'overlay' }, b)).toEqual({
			nodes: [a, c],
			view: 'overlay'
		});
	});
});

describe('combine', () => {
	const series = [
		{
			nodeId: a,
			points: [
				{ time: 1, value: 1 },
				{ time: 2, value: 4 }
			]
		},
		{
			nodeId: a,
			points: [
				{ time: 1, value: 3 },
				{ time: 3, value: 5 }
			]
		},
		{ nodeId: b, points: [{ time: 1, value: 7 }] }
	];

	it('sums the series of a node per time (interfaces)', () => {
		const out = combine(series, 'sum');
		expect(out.get(a)).toEqual([
			{ time: 1, value: 4 },
			{ time: 2, value: 4 },
			{ time: 3, value: 5 }
		]);
		expect(out.get(b)).toEqual([{ time: 1, value: 7 }]);
	});

	it('takes the maximum per time (busiest disk)', () => {
		expect(combine(series, 'max').get(a)).toEqual([
			{ time: 1, value: 3 },
			{ time: 2, value: 4 },
			{ time: 3, value: 5 }
		]);
	});

	it('transforms the values, e.g. idle into busy', () => {
		const out = combine([{ nodeId: a, points: [{ time: 1, value: 250_000 }] }], 'max', (v) =>
			Math.max(0, 1_000_000 - v)
		);
		expect(out.get(a)).toEqual([{ time: 1, value: 750_000 }]);
	});

	it('leaves out values that are no numbers', () => {
		const out = combine([{ nodeId: a, points: [{ time: 1, value: NaN }] }], 'sum');
		expect(out.get(a)).toEqual([]);
	});

	it('has nothing for a node without series', () => {
		expect(combine(series, 'sum').get(c)).toBeUndefined();
	});
});

describe('sharedMax', () => {
	it('is the largest value of all nodes, for one scale side by side', () => {
		const perNode = new Map([
			[a, [{ time: 1, value: 3 }]],
			[b, [{ time: 1, value: 9 }]]
		]);
		expect(sharedMax(perNode)).toBe(9);
	});

	it('is undefined without data', () => {
		expect(sharedMax(new Map())).toBeUndefined();
		expect(sharedMax(new Map([[a, []]]))).toBeUndefined();
	});
});
