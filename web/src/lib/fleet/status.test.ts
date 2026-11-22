import { create } from '@bufbuild/protobuf';
import { describe, expect, it } from 'vitest';
import {
	KeyFiguresSchema,
	NodeSummarySchema,
	type NodeSummary
} from '#lib/api/gen/sermon/web/v1/fleet_pb.js';
import { assess, countByStatus, filterNodes, groupsOf, tagsOf } from './status.js';

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
			filterNodes(nodes, { search: '', group: '', status: '', tag: '' }).map((n) => n.hostname)
		).toEqual(['web-01', 'web-03', 'db-01', 'web-02']);
	});

	it('filters by search, group and status', () => {
		expect(
			filterNodes(nodes, { search: ' DB ', group: '', status: '', tag: '' }).map((n) => n.hostname)
		).toEqual(['db-01']);
		expect(
			filterNodes(nodes, { search: '', group: 'g1', status: 'ok', tag: '' }).map((n) => n.hostname)
		).toEqual(['web-02']);
		expect(
			filterNodes(nodes, { search: 'debian 13', group: 'g2', status: '', tag: '' })
		).toHaveLength(1);
	});

	// M2-29: the tag filter takes one key=value pair; the search finds tags too.
	it('filters by tag and finds tags in the search', () => {
		const tagged = [
			{ ...node('web-01', true, 0.1), tags: { env: 'prod', role: 'web' } },
			{ ...node('web-02', true, 0.1), tags: { env: 'staging', role: 'web' } },
			node('db-01', true, 0.1, undefined, undefined, 'g2')
		] as NodeSummary[];
		const filter = { search: '', group: '', status: '' as const };
		expect(filterNodes(tagged, { ...filter, tag: 'env=prod' }).map((n) => n.hostname)).toEqual([
			'web-01'
		]);
		expect(filterNodes(tagged, { ...filter, tag: 'role=web' })).toHaveLength(2);
		expect(filterNodes(tagged, { ...filter, tag: 'env=test' })).toHaveLength(0);
		expect(
			filterNodes(tagged, { ...filter, search: 'staging', tag: '' }).map((n) => n.hostname)
		).toEqual(['web-02']);
		expect(tagsOf(tagged)).toEqual(['env=prod', 'env=staging', 'role=web']);
	});

	it('counts and lists groups', () => {
		expect(countByStatus(nodes)).toEqual({ ok: 1, warning: 1, critical: 1, offline: 1 });
		expect(groupsOf(nodes)).toEqual([
			{ id: 'g2', name: 'db' },
			{ id: 'g1', name: 'web' }
		]);
	});
});
