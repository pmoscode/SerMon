import { create, type MessageInitShape } from '@bufbuild/protobuf';
import { describe, expect, it } from 'vitest';
import { AlertSchema, AlertState, type Alert } from '#lib/api/gen/sermon/web/v1/alert_pb.js';
import { formatBytes, formatDuration, formatPercent } from '#lib/format/format.js';
import {
	alertValue,
	countAlerts,
	fieldLabel,
	fieldValue,
	filterAlerts,
	groupsOf,
	kindOf,
	ruleName,
	unverifiedSource
} from './view.js';

type Init = Exclude<MessageInitShape<typeof AlertSchema>, Alert>;

function alert(over: Init): Alert {
	return create(AlertSchema, {
		key: { ruleId: 'system.cpu.usage_high', nodeId: 'n1', dimension: '' },
		state: AlertState.WARNING,
		ruleName: { de: 'Hohe CPU-Auslastung', en: 'High CPU usage' },
		hostname: 'web-01',
		groupId: 'g1',
		groupName: 'web',
		...over
	} satisfies Init);
}

const list = [
	alert({ state: AlertState.CRITICAL }),
	alert({ state: AlertState.WARNING, hostname: 'db-01', groupId: 'g2', groupName: 'db' }),
	alert({
		state: AlertState.PENDING,
		key: { ruleId: 'system.memory.usage_high', nodeId: 'n1', dimension: '' },
		ruleName: { de: 'Hoher Speicherverbrauch', en: 'High memory usage' }
	}),
	alert({ state: AlertState.RESOLVED, dimensionLabels: { mountpoint: '/var' } })
];

describe('alert view', () => {
	it('sorts states into firing, pending and resolved', () => {
		expect(list.map(kindOf)).toEqual(['firing', 'firing', 'pending', 'resolved']);
		expect(countAlerts(list)).toEqual({ firing: 2, pending: 1, resolved: 1, critical: 1 });
	});

	it('filters by state, group and search over host, rule and dimension', () => {
		expect(filterAlerts(list, { state: 'firing', group: '', search: '' })).toHaveLength(2);
		expect(filterAlerts(list, { state: '', group: 'g2', search: '' })).toEqual([list[1]]);
		expect(filterAlerts(list, { state: '', group: '', search: 'DB-0' })).toEqual([list[1]]);
		expect(filterAlerts(list, { state: '', group: '', search: 'memory' })).toEqual([list[2]]);
		expect(filterAlerts(list, { state: '', group: '', search: '/var' })).toEqual([list[3]]);
		expect(filterAlerts(list, { state: '', group: '', search: 'Hohe CPU' })).toHaveLength(3);
	});

	it('lists the groups once, by name', () => {
		expect(groupsOf(list)).toEqual([
			{ id: 'g2', name: 'db' },
			{ id: 'g1', name: 'web' }
		]);
	});

	it('names the rule in the language, falling back to its ID', () => {
		expect(ruleName(list[0], 'de')).toBe('Hohe CPU-Auslastung');
		expect(ruleName(list[0], 'en')).toBe('High CPU usage');
		expect(ruleName(alert({ ruleName: undefined }), 'en')).toBe('system.cpu.usage_high');
	});

	it('names an unverified source in words and keeps other values', () => {
		expect(fieldLabel('source')).toBe('Source');
		expect(fieldValue('source', 'unverified')).toBe('not verified');
		expect(fieldValue('source', 'journal')).toBe('journal');
		expect(fieldValue('user', 'unverified')).toBe('unverified');
		expect(unverifiedSource({ source: 'unverified' })).toBe(true);
		expect(unverifiedSource({ user: 'unverified' })).toBe(false);
	});

	it('formats values in the units of the rules', () => {
		expect(alertValue('ratio', 0.964, 'en')).toBe(formatPercent(0.964, 'en'));
		expect(alertValue('hours', 5.25, 'de')).toBe('5,25 h');
		expect(alertValue('per_minute', 3, 'en')).toBe('3 /min');
		expect(alertValue('count', 12, 'en')).toBe('12');
		expect(alertValue('seconds', 90, 'en')).toBe(formatDuration(90, 'en'));
		expect(alertValue('bytes_per_second', 2048, 'en')).toBe(formatBytes(2048, 'en') + '/s');
		expect(alertValue('', 1234.5, 'en')).toBe('1,235');
	});
});
