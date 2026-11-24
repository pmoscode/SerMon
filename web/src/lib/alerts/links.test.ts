import { describe, expect, it } from 'vitest';
import { CHART_WINDOW, chartsQuery, parseNodeView, tabOfCategory } from './links.js';

const at = Date.parse('2026-11-24T10:00:00Z');

describe('links from alerts into the node page', () => {
	it('opens the area of the rule around the start, never beyond now', () => {
		const later = at + 5 * CHART_WINDOW;
		expect(chartsQuery(at, later, 'cpu')).toBe(
			'?from=2026-11-24T09%3A00%3A00.000Z&to=2026-11-24T11%3A00%3A00.000Z&tab=cpu'
		);
		// Started 10 minutes ago: up to now.
		expect(chartsQuery(at, at + 600_000, 'disk')).toBe(
			'?from=2026-11-24T09%3A00%3A00.000Z&to=2026-11-24T10%3A10%3A00.000Z&tab=disk'
		);
		// Just now: at least a minute after.
		expect(chartsQuery(at, at, undefined)).toBe(
			'?from=2026-11-24T09%3A00%3A00.000Z&to=2026-11-24T10%3A01%3A00.000Z'
		);
	});

	it('maps the categories that have an area', () => {
		expect(tabOfCategory('security')).toBe('security');
		expect(tabOfCategory('availability')).toBeUndefined();
		expect(tabOfCategory('system')).toBeUndefined();
		expect(tabOfCategory(undefined)).toBeUndefined();
	});

	it('reads the node page URL and drops invalid values', () => {
		const view = parseNodeView(
			new URLSearchParams(chartsQuery(at, at + CHART_WINDOW * 2, 'network'))
		);
		expect(view).toEqual({
			range: { from: at - CHART_WINDOW, to: at + CHART_WINDOW },
			tab: 'network'
		});
		expect(parseNodeView(new URLSearchParams('from=x&to=y&tab=evil'))).toEqual({});
		expect(
			parseNodeView(
				new URLSearchParams('from=2026-11-24T10:00:00Z&to=2026-11-24T09:00:00Z&tab=logs')
			)
		).toEqual({ tab: 'logs' });
	});
});
