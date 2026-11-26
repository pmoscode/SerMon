/**
 * The form of a widget in the editor: what the user picks, checked like the server checks it
 * (ADR 0040), so most mistakes show at the field instead of after saving.
 */
import { create } from '@bufbuild/protobuf';
import {
	VariableKind,
	WidgetKind,
	WidgetPositionSchema,
	WidgetQuerySchema,
	WidgetSchema,
	WidgetTargetSchema,
	type DashboardVariable,
	type Widget
} from '#lib/api/gen/sermon/web/v1/dashboard_pb.js';
import type { Box } from './layout.js';
import { isMetricKind, MAX_NODES, singleNode } from './model.js';

export type TargetMode = 'nodes' | 'group' | 'variable' | 'all';

export interface WidgetForm {
	id: string;
	kind: WidgetKind;
	title: string;
	metric: string;
	labels: Record<string, string>;
	target: TargetMode;
	nodeIds: string[];
	groupId: string;
	variable: string;
	text: string;
	minSeverity: number;
}

/** What is wrong: the field to fix. */
export type FormError = 'title' | 'metric' | 'target' | 'nodes' | 'single' | 'text';

export const MAX_TEXT = 256;

export function emptyForm(id: string, kind: WidgetKind): WidgetForm {
	return {
		id,
		kind,
		title: '',
		metric: '',
		labels: {},
		target: kind === WidgetKind.ALERT_LIST ? 'all' : 'nodes',
		nodeIds: [],
		groupId: '',
		variable: '',
		text: '',
		minSeverity: 0
	};
}

export function formOf(w: Widget): WidgetForm {
	const t = w.query?.target;
	const target: TargetMode = t?.nodeIds.length
		? 'nodes'
		: t?.groupId
			? 'group'
			: t?.variable
				? 'variable'
				: w.kind === WidgetKind.ALERT_LIST
					? 'all'
					: 'nodes';
	return {
		id: w.id,
		kind: w.kind,
		title: w.title,
		metric: w.query?.metric ?? '',
		labels: { ...(w.query?.labels ?? {}) },
		target,
		nodeIds: [...(t?.nodeIds ?? [])],
		groupId: t?.groupId ?? '',
		variable: t?.variable ?? '',
		text: w.query?.text ?? '',
		minSeverity: w.query?.minSeverity ?? 0
	};
}

function textBytes(s: string): number {
	return new TextEncoder().encode(s).length;
}

/** The widget of the form at the box, or the field that is wrong. */
export function widgetOf(
	f: WidgetForm,
	box: Box,
	variables: Pick<DashboardVariable, 'name' | 'kind'>[]
): { widget?: Widget; error?: FormError } {
	const metric = isMetricKind(f.kind);
	const title = f.title.trim();
	const text = f.text.trim();
	if (!title) return { error: 'title' };
	if (metric && !f.metric) return { error: 'metric' };
	const variable = variables.find((v) => v.name === f.variable);
	const target =
		f.target === 'nodes'
			? f.nodeIds.length > 0
			: f.target === 'group'
				? f.groupId !== ''
				: f.target === 'variable'
					? variable !== undefined
					: f.kind === WidgetKind.ALERT_LIST;
	if (!target) return { error: 'target' };
	if (f.target === 'nodes' && f.nodeIds.length > MAX_NODES) return { error: 'nodes' };
	const single =
		(f.target === 'nodes' && f.nodeIds.length === 1) ||
		(f.target === 'variable' && variable?.kind === VariableKind.NODE);
	if (singleNode(f.kind) && !single) return { error: 'single' };
	const logs = f.kind === WidgetKind.LOG_STREAM;
	if (logs && (textBytes(text) > MAX_TEXT || text.includes('\n'))) return { error: 'text' };
	const labels = metric
		? Object.fromEntries(
				Object.entries(f.labels)
					.filter(([, v]) => v.trim() !== '')
					.map(([k, v]) => [k, v.trim()])
			)
		: {};
	const widget = create(WidgetSchema, {
		id: f.id,
		kind: f.kind,
		title,
		position: create(WidgetPositionSchema, box),
		query: create(WidgetQuerySchema, {
			metric: metric ? f.metric : '',
			labels,
			text: logs ? text : '',
			minSeverity: logs ? f.minSeverity : 0,
			target: create(WidgetTargetSchema, {
				nodeIds: f.target === 'nodes' ? f.nodeIds : [],
				groupId: f.target === 'group' ? f.groupId : '',
				variable: f.target === 'variable' ? f.variable : ''
			})
		})
	});
	return { widget };
}
