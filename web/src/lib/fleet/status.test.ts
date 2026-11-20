import { create } from '@bufbuild/protobuf';
import { describe, expect, it } from 'vitest';
import {
	KeyFiguresSchema,
	NodeSummarySchema,
	type NodeSummary
} from '#lib/api/gen/sermon/web/v1/fleet_pb.js';
import { assess, countByStatus, filterNodes, groupsOf } from './status.js';

function node(
	hostname: string,
	online: boolean,
	cpu?: number,
	memory?: number,
	fs?: number,
	group = 'g1'
): NodeSummary {
	return create(NodeSummarySchema, {
		nodeId: hostname,
		hostname,
		groupId: group,
		groupName: group === 'g1' ? 'web' : 'db',
		osId: 'debian',
		osVersion: '13',
		online,
		figures: create(KeyFiguresSchema, {
			cpuBusyRatio: cpu,
			memoryUsedRatio: memory,
			filesystemMaxUsedRatio: fs
		})
	});
}

describe('assess', () => {
	it.each([
		[node('a', false, 0.99), 'offline', []],
		[node('a', true, 0.2, 0.5, 0.3), 'ok', []],
		[node('a', true, 0.85), 'warning', ['cpu']],
		[node('a', true, 0.81, 0.95), 'critical', ['memory', 'cpu']],
		[node('a', true, undefined, undefined, 0.9), 'critical', ['filesystem']],
		[node('a', true), 'ok', []]
	])('%#', (n, status, reasons) => {
		expect(assess(n)).toEqual({ status, reasons });
	});
});

describe('filterNodes', () => {
	const nodes = [
		node('web-02', true, 0.1),
		node('web-01', true, 0.95),
		node('db-01', false, undefined, undefined, undefined, 'g2'),
		node('web-03', true, 0.85)
	];

	it('puts problems first, then sorts by host name', () => {
		expect(
			filterNodes(nodes, { search: '', group: '', status: '' }).map((n) => n.hostname)
		).toEqual(['web-01', 'web-03', 'db-01', 'web-02']);
	});

	it('filters by search, group and status', () => {
		expect(
			filterNodes(nodes, { search: ' DB ', group: '', status: '' }).map((n) => n.hostname)
		).toEqual(['db-01']);
		expect(
			filterNodes(nodes, { search: '', group: 'g1', status: 'ok' }).map((n) => n.hostname)
		).toEqual(['web-02']);
		expect(filterNodes(nodes, { search: 'debian 13', group: 'g2', status: '' })).toHaveLength(1);
	});

	it('counts and lists groups', () => {
		expect(countByStatus(nodes)).toEqual({ ok: 1, warning: 1, critical: 1, offline: 1 });
		expect(groupsOf(nodes)).toEqual([
			{ id: 'g2', name: 'db' },
			{ id: 'g1', name: 'web' }
		]);
	});
});
