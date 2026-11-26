import { describe, expect, it } from 'vitest';
import {
	MAX_RANGE,
	alertLogsQuery,
	emptyFilter,
	filterParams,
	logsQuery,
	parseFilter,
	rangeOf,
	type LogFilter
} from './filter.js';

const node = '0193f5a0-0000-7000-8000-000000000001';
const other = '0193f5a0-0000-7000-8000-000000000002';
const group = '0193f5a0-0000-7000-8000-0000000000aa';
const now = Date.parse('2026-11-23T12:00:00Z');

function roundTrip(f: LogFilter): LogFilter {
	return parseFilter(new URLSearchParams(filterParams(f)));
}

describe('filter ↔ URL', () => {
	it('starts empty with the last hour', () => {
		expect(parseFilter(new URLSearchParams())).toEqual(emptyFilter());
		expect(filterParams(emptyFilter()).toString()).toBe('');
	});

	it('keeps every filter in the URL', () => {
		const f: LogFilter = {
			preset: '6h',
			nodes: [node, other],
			group,
			sources: ['file'],
			units: ['nginx.service', 'cron'],
			severity: 'warning',
			text: 'timed out & "x"',
			ignoreCase: true
		};
		const params = filterParams(f);
		expect(params.toString()).toBe(
			`range=6h&node=${node}&node=${other}&group=${group}&source=file&unit=nginx.service&unit=cron&severity=warning&q=timed+out+%26+%22x%22&icase=1`
		);
		expect(roundTrip(f)).toEqual(f);
	});

	it('keeps an absolute range as ISO times', () => {
		const f = { ...emptyFilter(), from: now - 600_000, to: now };
		expect(filterParams(f).get('from')).toBe('2026-11-23T11:50:00.000Z');
		expect(roundTrip(f)).toEqual(f);
	});

	it('drops what is not valid', () => {
		const f = parseFilter(
			new URLSearchParams(
				'range=30d&node=x&node=' +
					node +
					'&group=nope&source=docker&source=journal&severity=loud&from=yesterday&to=' +
					now +
					'&unit=&q=' +
					'x'.repeat(300)
			)
		);
		expect(f.preset).toBe('1h');
		expect(f.nodes).toEqual([node]);
		expect(f.group).toBeUndefined();
		expect(f.sources).toEqual(['journal']);
		expect(f.severity).toBeUndefined();
		expect(f.from).toBeUndefined();
		expect(f.to).toBeUndefined();
		expect(f.units).toEqual([]);
		expect(new TextEncoder().encode(f.text).length).toBeLessThanOrEqual(256);
	});

	it('keeps at most 100 nodes and 20 units', () => {
		const params = new URLSearchParams();
		for (let i = 0; i < 150; i++)
			params.append('node', `0193f5a0-0000-7000-8000-${String(i).padStart(12, '0')}`);
		for (let i = 0; i < 30; i++) params.append('unit', `u${i}`);
		const f = parseFilter(params);
		expect(f.nodes).toHaveLength(100);
		expect(f.units).toHaveLength(20);
	});
});

describe('rangeOf', () => {
	it('is the preset up to now or the absolute range', () => {
		expect(rangeOf({ ...emptyFilter(), preset: '15m' }, now)).toEqual({
			from: now - 900_000,
			to: now
		});
		expect(rangeOf({ ...emptyFilter(), from: now - 5000, to: now - 1000 }, now)).toEqual({
			from: now - 5000,
			to: now - 1000
		});
	});

	it('is at most 14 days and never reversed', () => {
		const long = rangeOf({ ...emptyFilter(), from: now - 30 * 86400_000, to: now }, now);
		expect(long).toEqual({ from: now - MAX_RANGE, to: now });
		const reversed = rangeOf({ ...emptyFilter(), from: now, to: now - 1000 }, now);
		expect(reversed.to).toBeGreaterThan(reversed.from);
	});
});

describe('links into the log explorer', () => {
	it('opens the logs of a node in a range', () => {
		expect(logsQuery(node, now - 3600_000, now)).toBe(
			`?from=2026-11-23T11%3A00%3A00.000Z&to=2026-11-23T12%3A00%3A00.000Z&node=${node}`
		);
		// Charts may show 30 days: the link keeps the last 14.
		const params = new URLSearchParams(logsQuery(node, now - 30 * 86400_000, now));
		expect(Date.parse(params.get('from')!)).toBe(now - MAX_RANGE);
	});

	it('opens the logs around the start of an alert', () => {
		const since = now - 3600_000;
		const params = new URLSearchParams(alertLogsQuery(node, since, now));
		expect(Date.parse(params.get('from')!)).toBe(since - 15 * 60_000);
		expect(Date.parse(params.get('to')!)).toBe(since + 15 * 60_000);
		// A fresh alert: up to now.
		const fresh = new URLSearchParams(alertLogsQuery(node, now - 60_000, now));
		expect(Date.parse(fresh.get('to')!)).toBe(now);
		expect(fresh.getAll('unit')).toEqual([]);
		// M4-28: an alert of a unit shows its lines, the filter reads them back.
		const unit = alertLogsQuery(node, since, now, 'e2e migrate&x.service');
		expect(new URLSearchParams(unit).getAll('unit')).toEqual(['e2e migrate&x.service']);
		expect(
			parseFilter(new URLSearchParams(alertLogsQuery(node, since, now, 'cron.service'))).units
		).toEqual(['cron.service']);
	});
});
