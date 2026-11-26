/**
 * The form of a module's configuration (ADR 0039, M5-07), built from the module's JSON schema –
 * the subset the agent and the server check (agent/crates/sermon-modules/src/schema.rs). The
 * server checks the values and names the property; the agent's answer counts. Credentials
 * (writeOnly) never become part of the configuration and their values are never shown.
 */

export type FieldKind = 'string' | 'integer' | 'number' | 'boolean' | 'strings' | 'integers';

export interface Field {
	name: string;
	kind: FieldKind;
	title?: string;
	description?: string;
	required: boolean;
	/** A credential: a password field, sent apart from the configuration. */
	secret: boolean;
	default?: unknown;
	/** For the input's maxlength. */
	maxLength?: number;
	values?: string[];
}

/** What the form hands over: the setting and the credentials typed in. */
export interface Saved {
	enabled: boolean;
	/** Seconds; undefined takes the module's default. */
	intervalSeconds?: number;
	configJson: string;
	/** Credentials typed in (field → value); empty fields keep what is stored. */
	secrets: Record<string, string>;
	/** Credentials whose stored value goes. */
	clearSecrets: string[];
}

/** What the form holds: text for inputs and lists (one item per line), a flag for checkboxes. */
export type Values = Record<string, string | boolean>;

export type Problem =
	| { kind: 'required' }
	| { kind: 'integer' }
	| { kind: 'number' }
	| { kind: 'item'; index: number; problem: Problem };

type Raw = Record<string, unknown>;

const num = (v: unknown) => (typeof v === 'number' ? v : undefined);

function textOf(field: Field, p: Raw) {
	field.maxLength = num(p.maxLength);
	if (Array.isArray(p.enum))
		field.values = p.enum.filter((v): v is string => typeof v === 'string');
}

/** The fields of a schema in the order it lists them; nothing for a schema that isn't one. */
export function fieldsOf(schemaJson: string): Field[] {
	let root: Raw;
	try {
		root = JSON.parse(schemaJson) as Raw;
	} catch {
		return [];
	}
	const props = root?.properties;
	if (typeof props !== 'object' || props === null) return [];
	const required = Array.isArray(root.required) ? root.required : [];
	const out: Field[] = [];
	for (const [name, value] of Object.entries(props as Record<string, Raw>)) {
		const p = value ?? {};
		const items = (p.items ?? {}) as Raw;
		let kind: FieldKind;
		if (p.type === 'array') kind = items.type === 'integer' ? 'integers' : 'strings';
		else if (p.type === 'integer' || p.type === 'number' || p.type === 'boolean') kind = p.type;
		else kind = 'string';
		const field: Field = {
			name,
			kind,
			title: typeof p.title === 'string' ? p.title : undefined,
			description: typeof p.description === 'string' ? p.description : undefined,
			required: required.includes(name),
			secret: p.writeOnly === true,
			default: p.default
		};
		if (kind === 'string') textOf(field, p);
		if (kind === 'strings') textOf(field, items);
		out.push(field);
	}
	return out;
}

function show(v: unknown): string {
	if (Array.isArray(v)) return v.map(String).join('\n');
	return v === undefined || v === null ? '' : String(v);
}

/**
 * The form's values: the stored configuration, else a suggestion of the agent, else empty (the
 * defaults show as placeholders). Credentials always start empty.
 */
export function valuesOf(fields: Field[], configJson: string, suggestion = ''): Values {
	const parse = (s: string): Raw => {
		try {
			const v = JSON.parse(s || '{}') as unknown;
			return typeof v === 'object' && v !== null && !Array.isArray(v) ? (v as Raw) : {};
		} catch {
			return {};
		}
	};
	const stored = parse(configJson);
	const start = Object.keys(stored).length > 0 ? stored : parse(suggestion);
	const values: Values = {};
	for (const f of fields) {
		if (f.kind === 'boolean')
			values[f.name] =
				typeof start[f.name] === 'boolean' ? (start[f.name] as boolean) : f.default === true;
		else values[f.name] = f.secret ? '' : show(start[f.name]);
	}
	return values;
}

/** The placeholder of a field: its default. */
export function placeholderOf(f: Field): string {
	return f.secret || f.default === undefined ? '' : show(f.default);
}

function numberProblem(s: string, integer: boolean): Problem | undefined {
	if (integer ? !/^-?\d+$/.test(s) : !/^-?\d+(\.\d+)?$/.test(s))
		return { kind: integer ? 'integer' : 'number' };
	return undefined;
}

const lines = (s: string) =>
	s
		.split('\n')
		.map((l) => l.trim())
		.filter((l) => l !== '');

/**
 * What keeps a field's value from being sent: a missing required value or a number that isn't
 * one. The rest of the schema (lengths, patterns, ranges, formats) the server checks with the
 * agent's rules and names the property (no third copy of the checker here); secretSet says a
 * stored credential counts as given.
 */
export function problemOf(
	f: Field,
	value: string | boolean,
	secretSet = false
): Problem | undefined {
	if (f.kind === 'boolean') return undefined;
	const s = String(value).trim();
	if (s === '') return f.required && !(f.secret && secretSet) ? { kind: 'required' } : undefined;
	if (f.kind === 'integer' || f.kind === 'number') return numberProblem(s, f.kind === 'integer');
	if (f.kind === 'integers')
		for (const [i, item] of lines(s).entries()) {
			const p = numberProblem(item, true);
			if (p) return { kind: 'item', index: i + 1, problem: p };
		}
	return undefined;
}

/** The problems of the form by field. */
export function problems(
	fields: Field[],
	values: Values,
	secretsSet: string[] = []
): Map<string, Problem> {
	const out = new Map<string, Problem>();
	for (const f of fields) {
		const p = problemOf(f, values[f.name] ?? '', secretsSet.includes(f.name));
		if (p) out.set(f.name, p);
	}
	return out;
}

/**
 * The configuration as JSON: the fields given (empty ones take the agent's default), never a
 * credential. A checkbox that matches its default is left out too.
 */
export function configOf(fields: Field[], values: Values): string {
	const config: Raw = {};
	for (const f of fields) {
		if (f.secret) continue;
		const v = values[f.name];
		if (f.kind === 'boolean') {
			if (v !== (f.default === true)) config[f.name] = v === true;
			continue;
		}
		const s = String(v ?? '').trim();
		if (s === '') continue;
		if (f.kind === 'integer' || f.kind === 'number') config[f.name] = Number(s);
		else if (f.kind === 'strings') config[f.name] = lines(s);
		else if (f.kind === 'integers') config[f.name] = lines(s).map(Number);
		else config[f.name] = s;
	}
	return JSON.stringify(config);
}

/** The credentials typed in (field → value); empty ones keep what is stored. */
export function secretsOf(fields: Field[], values: Values): Record<string, string> {
	const out: Record<string, string> = {};
	for (const f of fields) {
		const v = values[f.name];
		if (f.secret && typeof v === 'string' && v !== '') out[f.name] = v;
	}
	return out;
}
