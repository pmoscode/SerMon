import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import {
	configOf,
	fieldsOf,
	placeholderOf,
	problemOf,
	problems,
	secretsOf,
	valuesOf,
	type Field
} from './schema.js';

/** A value typed into a password field; for tests only. */
const TYPED = 'typed-in-value'; // betterleaks:allow

const SCHEMA = JSON.stringify({
	type: 'object',
	additionalProperties: false,
	required: ['url', 'password'],
	properties: {
		url: { type: 'string', format: 'uri', maxLength: 64, default: 'http://127.0.0.1/status' },
		timeout: { type: 'string', format: 'duration', maxLength: 8, default: '5s' },
		addr: { type: 'string', format: 'host-port', maxLength: 64 },
		port: { type: 'integer', minimum: 1, maximum: 65535, title: 'Port' },
		ratio: { type: 'number', minimum: 0, maximum: 1 },
		verify: { type: 'boolean', default: true },
		mode: { type: 'string', enum: ['plain', 'tls'], maxLength: 8 },
		user: { type: 'string', pattern: '^[a-z_][a-z0-9_]*$', minLength: 2, maxLength: 16 },
		password: { type: 'string', writeOnly: true, maxLength: 64 },
		paths: { type: 'array', maxItems: 2, items: { type: 'string', format: 'path', maxLength: 64 } },
		codes: { type: 'array', maxItems: 3, items: { type: 'integer', minimum: 100, maximum: 599 } }
	}
});

const fields = fieldsOf(SCHEMA);
const field = (name: string) => fields.find((f) => f.name === name) as Field;

describe('fieldsOf', () => {
	it('reads every kind of field in the order of the schema', () => {
		expect(fields.map((f) => [f.name, f.kind])).toEqual([
			['url', 'string'],
			['timeout', 'string'],
			['addr', 'string'],
			['port', 'integer'],
			['ratio', 'number'],
			['verify', 'boolean'],
			['mode', 'string'],
			['user', 'string'],
			['password', 'string'],
			['paths', 'strings'],
			['codes', 'integers']
		]);
		expect(field('password')).toMatchObject({ secret: true, required: true });
		expect(field('url')).toMatchObject({ required: true, maxLength: 64 });
		expect(field('mode').values).toEqual(['plain', 'tls']);
		expect(field('port').title).toBe('Port');
	});

	it('gives nothing for something that is no schema', () => {
		expect(fieldsOf('nope')).toEqual([]);
		expect(fieldsOf('{"type":"object"}')).toEqual([]);
	});
});

describe('valuesOf', () => {
	it('starts with the stored configuration, never with a credential', () => {
		const v = valuesOf(
			fields,
			'{"url":"http://x/","verify":false,"codes":[200,301],"password":"leak"}'
		);
		expect(v).toMatchObject({
			url: 'http://x/',
			verify: false,
			codes: '200\n301',
			password: '',
			port: ''
		});
	});

	it('takes the suggestion of the agent while nothing is stored', () => {
		expect(valuesOf(fields, '{}', '{"port":8080}').port).toBe('8080');
		expect(valuesOf(fields, '{"port":80}', '{"port":8080}').port).toBe('80');
		expect(valuesOf(fields, 'broken', 'broken').verify).toBe(true);
	});

	it('shows defaults as placeholders, not as values', () => {
		expect(valuesOf(fields, '').url).toBe('');
		expect(placeholderOf(field('url'))).toBe('http://127.0.0.1/status');
		expect(placeholderOf(field('password'))).toBe('');
	});
});

describe('problemOf', () => {
	it('accepts what it can send – the server checks the rest with the agent rules', () => {
		for (const [name, value] of [
			['url', 'https://[::1]:8443/x'],
			['url', 'ftp://checked-by-the-server/'],
			['port', '65535'],
			['port', '0'],
			['ratio', '0.5'],
			['codes', '200\n 301 \n'],
			['port', ''],
			['verify', false]
		] as const)
			expect(problemOf(field(name), value), `${name}=${value}`).toBeUndefined();
	});

	it('names what it can not send', () => {
		for (const [name, value, kind] of [
			['url', '', 'required'],
			['url', '  ', 'required'],
			['port', '8.5', 'integer'],
			['port', 'eighty', 'integer'],
			['ratio', 'x', 'number'],
			['codes', '200\nseven', 'item'],
			['password', '', 'required']
		] as const)
			expect(problemOf(field(name), value)?.kind, `${name}=${value}`).toBe(kind);
		expect(problemOf(field('codes'), '200\nseven')).toEqual({
			kind: 'item',
			index: 2,
			problem: { kind: 'integer' }
		});
	});

	it('takes a stored credential as given', () => {
		expect(problemOf(field('password'), '', true)).toBeUndefined();
		const p = problems(fields, valuesOf(fields, '{"url":"http://x/"}'), ['password']);
		expect([...p.keys()]).toEqual([]);
		expect([...problems(fields, valuesOf(fields, '{}')).keys()]).toEqual(['url', 'password']);
	});
});

describe('configOf and secretsOf', () => {
	it('sends what was given in the types of the schema, never a credential', () => {
		const v = valuesOf(fields, '');
		Object.assign(v, {
			url: ' http://x/ ',
			port: '80',
			ratio: '0.25',
			verify: false,
			paths: '/a\n\n/b',
			codes: '200',
			password: TYPED
		});
		expect(JSON.parse(configOf(fields, v))).toEqual({
			url: 'http://x/',
			port: 80,
			ratio: 0.25,
			verify: false,
			paths: ['/a', '/b'],
			codes: [200]
		});
		expect(configOf(fields, v)).not.toContain(TYPED);
		expect(secretsOf(fields, v)).toEqual({ password: TYPED });
	});

	it('leaves out empty fields and checkboxes at their default', () => {
		expect(configOf(fields, valuesOf(fields, ''))).toBe('{}');
		expect(secretsOf(fields, valuesOf(fields, ''))).toEqual({});
	});
});

// Every module the server knows (the agent's schemas, copied by just gen) has a form: its fields
// are of the subset, and they are labelled in German and English (#lib/modules/labels.ts).
describe('the modules of the build', () => {
	const dir = join(__dirname, '../../../../server/internal/domain/module/schemas');
	const ids = readdirSync(dir)
		.filter((f) => f.endsWith('.json'))
		.map((f) => f.slice(0, -5));

	it('are read', () => {
		expect(ids).toContain('simulate');
		for (const id of ids) {
			const f = fieldsOf(readFileSync(join(dir, `${id}.json`), 'utf8'));
			expect(f.length, id).toBeGreaterThan(0);
		}
	});

	it('have labels in both languages', async () => {
		const messages = (lang: string) =>
			JSON.parse(readFileSync(join(__dirname, `../../../messages/${lang}.json`), 'utf8')) as Record<
				string,
				string
			>;
		for (const lang of ['de', 'en']) {
			const msg = messages(lang);
			for (const id of ids) {
				expect(msg[`module_${id}`], `${lang}: module_${id}`).toBeTruthy();
				for (const f of fieldsOf(readFileSync(join(dir, `${id}.json`), 'utf8')))
					expect(msg[`module_${id}_${f.name}`], `${lang}: module_${id}_${f.name}`).toBeTruthy();
			}
		}
		const { moduleLabel, fieldLabel } = await import('./labels.js');
		for (const id of ids) {
			expect(moduleLabel(id)).not.toBe(id);
			for (const f of fieldsOf(readFileSync(join(dir, `${id}.json`), 'utf8')))
				expect(fieldLabel(id, f), `${id}.${f.name}`).not.toBe(f.name);
		}
		// The first import of labels.js compiles the messages of every module: on CI's runners that
		// took longer than the default 5 s.
	}, 30_000);
});
