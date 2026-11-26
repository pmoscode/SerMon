import { describe, expect, it } from 'vitest';
import { enableQuery, openingOf } from './link.js';

const group = '0193f5a0-0000-7000-8000-0000000000aa';

describe('enableQuery and openingOf', () => {
	it('round-trip a group, a module and the agent suggestion', () => {
		const q = new URLSearchParams(enableQuery(group, 'nginx', '{"port":8080}'));
		expect(openingOf(q)).toEqual({ group, module: 'nginx', suggestion: '{"port":8080}' });
		expect(openingOf(new URLSearchParams(enableQuery(group, 'nginx')))).toEqual({
			group,
			module: 'nginx',
			suggestion: ''
		});
	});

	it('ignore other tabs and edited links', () => {
		for (const q of [
			'tab=logs&group=' + group + '&module=nginx',
			'tab=modules&group=x&module=nginx',
			'tab=modules&group=' + group + '&module=Nginx',
			'tab=modules'
		])
			expect(openingOf(new URLSearchParams(q)), q).toBeUndefined();
		const long = new URLSearchParams(enableQuery(group, 'nginx', 'x'.repeat(2000)));
		expect(openingOf(long)?.suggestion).toBe('');
	});
});
