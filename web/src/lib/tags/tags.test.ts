import { describe, expect, it } from 'vitest';
import { MAX_TAGS, parseTags, tagsText, tagPairs } from './tags';

describe('parseTags', () => {
	it('reads key=value pairs separated by commas, spaces or lines', () => {
		expect(parseTags('env=prod, role=db\nsite=fra-1')).toEqual({
			tags: { env: 'prod', role: 'db', site: 'fra-1' }
		});
		expect(parseTags('  ')).toEqual({ tags: {} });
	});

	it('takes the characters the server takes', () => {
		expect(parseTags('site=fra-1.dc:rack/7 team_2-a=Ops')).toEqual({
			tags: { site: 'fra-1.dc:rack/7', 'team_2-a': 'Ops' }
		});
	});

	it('names the first bad pair', () => {
		expect(parseTags('env=prod Env=prod')).toEqual({ error: 'Env=prod' });
		expect(parseTags('env=')).toEqual({ error: 'env=' });
		expect(parseTags('role')).toEqual({ error: 'role' });
		expect(parseTags('1env=prod')).toEqual({ error: '1env=prod' });
		expect(parseTags('env=<b>')).toEqual({ error: 'env=<b>' });
		expect(parseTags(`env=${'v'.repeat(64)}`)).toEqual({ error: `env=${'v'.repeat(64)}` });
	});

	it('refuses a key twice and more than the limit', () => {
		expect(parseTags('env=prod env=staging')).toEqual({ error: 'env=staging' });
		const many = Array.from({ length: MAX_TAGS + 1 }, (_, i) => `k${i}=v`).join(' ');
		expect(parseTags(many)).toEqual({ error: `k${MAX_TAGS}=v` });
	});
});

describe('tagsText and tagPairs', () => {
	it('sort by key', () => {
		expect(tagsText({ role: 'db', env: 'prod' })).toBe('env=prod, role=db');
		expect(tagPairs({ role: 'db', env: 'prod' })).toEqual(['env=prod', 'role=db']);
		expect(tagsText({})).toBe('');
	});

	it('round-trip', () => {
		const tags = { env: 'prod', site: 'fra-1' };
		expect(parseTags(tagsText(tags))).toEqual({ tags });
	});
});
