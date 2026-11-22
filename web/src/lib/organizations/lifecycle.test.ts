import { describe, expect, it } from 'vitest';
import { OrganizationStatus } from '#lib/api/gen/sermon/web/v1/organization_pb.js';
import { actionsFor, confirmed } from './lifecycle.js';

describe('actionsFor', () => {
	// M2-33, ADR 0029: delete and export only while locked; unlock lifts a scheduled deletion too.
	it('offers what the state allows', () => {
		expect(actionsFor(OrganizationStatus.ACTIVE)).toEqual(['lock', 'schedule']);
		expect(actionsFor(OrganizationStatus.LOCKED)).toEqual([
			'unlock',
			'schedule',
			'export',
			'delete'
		]);
		expect(actionsFor(OrganizationStatus.DELETION_SCHEDULED)).toEqual([
			'unlock',
			'export',
			'delete'
		]);
		expect(actionsFor(OrganizationStatus.UNSPECIFIED)).toEqual([]);
	});
});

describe('confirmed', () => {
	it('wants the exact name, apart from surrounding spaces', () => {
		expect(confirmed('Beta AG', ' Beta AG ')).toBe(true);
		expect(confirmed('Beta AG', 'beta ag')).toBe(false);
		expect(confirmed('Beta AG', 'Beta')).toBe(false);
		expect(confirmed('Beta AG', '')).toBe(false);
	});
});
