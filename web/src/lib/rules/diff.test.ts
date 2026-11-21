import { describe, expect, it } from 'vitest';
import { diffLines } from './diff.js';

describe('diffLines', () => {
	it('marks removed and added lines and keeps the rest', () => {
		const before = 'a\nwindow: 10m\nc\n';
		const after = 'a\nwindow: 15m\nc\nd\n';
		expect(diffLines(before, after)).toEqual([
			{ kind: 'same', text: 'a' },
			{ kind: 'del', text: 'window: 10m' },
			{ kind: 'add', text: 'window: 15m' },
			{ kind: 'same', text: 'c' },
			{ kind: 'add', text: 'd' }
		]);
	});

	it('shows a new rule as all added and equal texts as unchanged', () => {
		expect(diffLines('', 'x\ny')).toEqual([
			{ kind: 'add', text: 'x' },
			{ kind: 'add', text: 'y' }
		]);
		expect(diffLines('x\n', 'x\n').every((l) => l.kind === 'same')).toBe(true);
	});

	it('stays fast for long texts', () => {
		const long = Array.from({ length: 2000 }, (_, i) => `line ${i}`).join('\n');
		const start = performance.now();
		const out = diffLines(long, long.replace('line 1000', 'changed'));
		expect(out.filter((l) => l.kind !== 'same')).toHaveLength(2);
		expect(performance.now() - start).toBeLessThan(500);
	});
});
