import { describe, expect, it } from 'vitest';
import { create } from '@bufbuild/protobuf';
import {
	DashboardSpecSchema,
	VariableKind,
	WidgetKind,
	type Widget
} from '#lib/api/gen/sermon/web/v1/dashboard_pb.js';
import {
	resolveVariables,
	targetOf,
	variableParam,
	withVariable,
	type FleetNode
} from './variables.js';

const web1 = '0193f5a0-0000-7000-8000-000000000001';
const web2 = '0193f5a0-0000-7000-8000-000000000002';
const db1 = '0193f5a0-0000-7000-8000-000000000003';
const gWeb = '0193f5a0-0000-7000-8000-0000000000aa';
const gDb = '0193f5a0-0000-7000-8000-0000000000bb';
const fleet: FleetNode[] = [
	{ id: web1, hostname: 'web-01', groupId: gWeb, groupName: 'web' },
	{ id: web2, hostname: 'web-02', groupId: gWeb, groupName: 'web' },
	{ id: db1, hostname: 'db-01', groupId: gDb, groupName: 'db' }
];
const spec = create(DashboardSpecSchema, {
	version: 1,
	title: 'x',
	variables: [
		{ name: 'node', kind: VariableKind.NODE, defaultId: web2 },
		{ name: 'group', kind: VariableKind.GROUP },
		{ name: 'gone', kind: VariableKind.NODE, defaultId: '0193f5a0-0000-7000-8000-0000000000ff' }
	]
});

function widget(
	target: Partial<{ nodeIds: string[]; groupId: string; variable: string }>,
	kind = WidgetKind.TIMESERIES
): Widget {
	return {
		id: 'w',
		kind,
		title: 'W',
		query: {
			metric: 'm',
			labels: {},
			text: '',
			minSeverity: 0,
			target: { nodeIds: [], groupId: '', variable: '', ...target }
		}
	} as unknown as Widget;
}

describe('dashboard variables', () => {
	it('takes the choice of the URL, else the default, else the first node or group', () => {
		expect(resolveVariables(spec, new URLSearchParams(), fleet)).toEqual({
			node: web2,
			group: gDb,
			gone: db1
		});
		const params = new URLSearchParams({ 'var-node': db1, 'var-group': gWeb });
		expect(resolveVariables(spec, params, fleet)).toMatchObject({ node: db1, group: gWeb });
	});

	it('ignores choices of nodes or groups the viewer does not see', () => {
		const params = new URLSearchParams({
			'var-node': '0193f5a0-0000-7000-8000-0000000000ee',
			'var-group': web1
		});
		expect(resolveVariables(spec, params, fleet)).toMatchObject({ node: web2, group: gDb });
		expect(resolveVariables(spec, new URLSearchParams(), [])).toEqual({
			node: undefined,
			group: undefined,
			gone: undefined
		});
	});

	it('resolves the nodes of a widget', () => {
		const values = { node: web2, group: gWeb };
		expect(targetOf(widget({ nodeIds: [web1, db1] }), values, fleet)).toEqual({
			nodeIds: [web1, db1]
		});
		expect(targetOf(widget({ groupId: gWeb }), values, fleet)).toEqual({
			nodeIds: [web1, web2],
			groupId: gWeb
		});
		expect(targetOf(widget({ variable: 'node' }), values, fleet)).toEqual({ nodeIds: [web2] });
		expect(targetOf(widget({ variable: 'group' }), values, fleet)).toEqual({
			nodeIds: [web1, web2],
			groupId: gWeb
		});
		expect(targetOf(widget({ variable: 'group' }), { group: undefined }, fleet)).toEqual({
			nodeIds: []
		});
		expect(targetOf(widget({}, WidgetKind.ALERT_LIST), values, fleet)).toEqual({
			all: true,
			nodeIds: []
		});
	});

	it('keeps the choice in the URL', () => {
		const url = new URL('https://x.example/dashboards/d?var-node=a&edit=1');
		expect(variableParam('node')).toBe('var-node');
		expect(withVariable(url, 'node', web1).search).toBe(`?var-node=${web1}&edit=1`);
		expect(withVariable(url, 'group', '').search).toBe('?var-node=a&edit=1');
	});
});
