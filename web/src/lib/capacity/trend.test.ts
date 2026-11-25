import { describe, expect, it } from 'vitest';
import { daysUntil, topBy, trendOf } from './trend.js';

describe('capacity trends', () => {
	it('compares the last week with the first', () => {
		const days = Array.from({ length: 30 }, (_, i) => (i < 7 ? 0.2 : i >= 23 ? 0.5 : 0.3));
		const t = trendOf(days);
		expect(t?.recent).toBeCloseTo(0.5);
		expect(t?.change).toBeCloseTo(0.3);
		expect(trendOf([0.1, 0.3])).toEqual({ recent: 0.2 });
		expect(trendOf(Array(13).fill(0.4))?.change).toBeUndefined();
		expect(trendOf(Array(14).fill(0.4))?.change).toBeCloseTo(0);
		expect(trendOf([])).toBeUndefined();
	});

	it('ranks the top consumers, nodes without a value last', () => {
		const nodes = [
			{ nodeId: 'a', hostname: 'a', cpu: { recent: 0.2 }, memory: { recent: 0.9 } },
			{ nodeId: 'b', hostname: 'b' },
			// A tie: by host name, whatever the order.
			{ nodeId: 'd', hostname: 'd', cpu: { recent: 0.7 } },
			{ nodeId: 'c', hostname: 'c', cpu: { recent: 0.7 }, memory: { recent: 0.1 } }
		];
		expect(topBy(nodes, 'cpu').map((n) => n.nodeId)).toEqual(['c', 'd', 'a', 'b']);
		expect(topBy(nodes, 'memory').map((n) => n.nodeId)).toEqual(['a', 'c', 'b', 'd']);
	});

	it('counts the days until full', () => {
		expect(daysUntil(undefined, 0)).toBeUndefined();
		expect(daysUntil(3 * 86_400_000, 86_400_000)).toBe(2);
		expect(daysUntil(0, 86_400_000)).toBe(0);
	});
});
