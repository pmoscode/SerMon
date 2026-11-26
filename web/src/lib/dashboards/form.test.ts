import { describe, expect, it } from 'vitest';
import { VariableKind, WidgetKind } from '#lib/api/gen/sermon/web/v1/dashboard_pb.js';
import { emptyForm, formOf, widgetOf, type WidgetForm } from './form.js';

const node1 = '0193f5a0-0000-7000-8000-000000000001';
const node2 = '0193f5a0-0000-7000-8000-000000000002';
const group = '0193f5a0-0000-7000-8000-0000000000aa';
const variables = [
	{ name: 'node', kind: VariableKind.NODE },
	{ name: 'group', kind: VariableKind.GROUP }
];
const position = { x: 0, y: 0, w: 6, h: 4 };

function form(change: Partial<WidgetForm>): WidgetForm {
	return {
		...emptyForm('cpu', WidgetKind.TIMESERIES),
		title: 'CPU',
		metric: 'sermon_cpu_utilization_ratio',
		target: 'nodes',
		nodeIds: [node1],
		...change
	};
}

describe('widget form', () => {
	it('builds a chart of fixed nodes with label filters (empty filters left out)', () => {
		const r = widgetOf(form({ labels: { mode: 'user', cpu: '' } }), position, variables);
		expect(r.error).toBeUndefined();
		expect(r.widget?.query?.metric).toBe('sermon_cpu_utilization_ratio');
		expect(r.widget?.query?.labels).toEqual({ mode: 'user' });
		expect(r.widget?.query?.target?.nodeIds).toEqual([node1]);
		expect(r.widget?.position).toMatchObject(position);
	});

	it('names what is missing or wrong', () => {
		const cases: [Partial<WidgetForm>, string][] = [
			[{ title: '  ' }, 'title'],
			[{ metric: '' }, 'metric'],
			[{ nodeIds: [] }, 'target'],
			[{ target: 'group', groupId: '' }, 'target'],
			[{ target: 'variable', variable: '' }, 'target'],
			[{ nodeIds: Array.from({ length: 21 }, (_, i) => `n${i}`) }, 'nodes'],
			[{ kind: WidgetKind.STAT, nodeIds: [node1, node2] }, 'single'],
			[{ kind: WidgetKind.GAUGE, target: 'group', groupId: group }, 'single'],
			[{ kind: WidgetKind.GAUGE, target: 'variable', variable: 'group' }, 'single'],
			[{ kind: WidgetKind.LOG_STREAM, metric: '', target: 'all' }, 'target'],
			[{ kind: WidgetKind.LOG_STREAM, metric: '', text: 'x'.repeat(257) }, 'text']
		];
		for (const [change, error] of cases)
			expect(widgetOf(form(change), position, variables).error, JSON.stringify(change)).toBe(error);
	});

	it('builds the other kinds with only their fields', () => {
		const stat = widgetOf(
			form({ kind: WidgetKind.STAT, target: 'variable', variable: 'node' }),
			position,
			variables
		);
		expect(stat.widget?.query?.target).toMatchObject({
			variable: 'node',
			nodeIds: [],
			groupId: ''
		});
		const logs = widgetOf(
			form({
				kind: WidgetKind.LOG_STREAM,
				target: 'group',
				groupId: group,
				text: ' error ',
				minSeverity: 4,
				labels: { mode: 'x' }
			}),
			position,
			variables
		);
		expect(logs.widget?.query).toMatchObject({
			metric: '',
			labels: {},
			text: 'error',
			minSeverity: 4
		});
		expect(logs.widget?.query?.target?.groupId).toBe(group);
		expect(logs.widget?.query?.labels).toEqual({});
		const alerts = widgetOf(
			form({ kind: WidgetKind.ALERT_LIST, target: 'all', text: 'x', minSeverity: 3 }),
			position,
			variables
		);
		expect(alerts.error).toBeUndefined();
		expect(alerts.widget?.query).toMatchObject({
			metric: '',
			text: '',
			minSeverity: 0,
			target: { nodeIds: [], groupId: '', variable: '' }
		});
	});

	it('reads a widget back into the form', () => {
		const built = widgetOf(form({ labels: { mode: 'user' } }), position, variables).widget!;
		expect(formOf(built)).toMatchObject({
			id: 'cpu',
			title: 'CPU',
			target: 'nodes',
			nodeIds: [node1],
			labels: { mode: 'user' }
		});
		const alerts = widgetOf(
			form({ kind: WidgetKind.ALERT_LIST, target: 'all' }),
			position,
			variables
		).widget!;
		expect(formOf(alerts).target).toBe('all');
	});
});
