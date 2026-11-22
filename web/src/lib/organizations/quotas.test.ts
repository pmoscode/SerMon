import { create } from '@bufbuild/protobuf';
import { describe, expect, it } from 'vitest';
import { OrganizationQuotasSchema } from '#lib/api/gen/sermon/web/v1/organization_pb.js';
import { QUOTA_FIELDS, quotaForm, quotasFrom, quotaLimit } from './quotas.js';

describe('quotas', () => {
	it('turns the form into quotas: empty is the instance default', () => {
		const q = quotasFrom({
			maxNodes: '20',
			maxMembers: '',
			retentionCapDays: ' 90 ',
			samplesPerSecond: '',
			seriesPerNode: '500'
		});
		expect(q).toEqual({
			ok: true,
			quotas: expect.objectContaining({ maxNodes: 20, retentionCapDays: 90, seriesPerNode: 500 })
		});
		if (q.ok) {
			expect(q.quotas.maxMembers).toBeUndefined();
			expect(q.quotas.samplesPerSecond).toBeUndefined();
		}
	});

	it('refuses what the server would refuse', () => {
		for (const bad of ['0', '-1', '1.5', 'abc', '1000001', '1e3']) {
			expect(quotasFrom({ ...quotaForm(undefined), maxNodes: bad }), bad).toEqual({
				ok: false,
				field: 'maxNodes'
			});
		}
		expect(quotasFrom({ ...quotaForm(undefined), seriesPerNode: '100001' })).toEqual({
			ok: false,
			field: 'seriesPerNode'
		});
		expect(quotasFrom({ ...quotaForm(undefined), retentionCapDays: '3650' }).ok).toBe(true);
	});

	it('fills the form from stored quotas and back', () => {
		const stored = create(OrganizationQuotasSchema, { maxNodes: 3, samplesPerSecond: 2000 });
		const form = quotaForm(stored);
		expect(form).toEqual({
			maxNodes: '3',
			maxMembers: '',
			retentionCapDays: '',
			samplesPerSecond: '2000',
			seriesPerNode: ''
		});
		expect(quotasFrom(form)).toEqual({
			ok: true,
			quotas: expect.objectContaining({ maxNodes: 3, samplesPerSecond: 2000 })
		});
		expect(QUOTA_FIELDS).toHaveLength(5);
	});

	it('reads a limit: undefined is the default', () => {
		expect(quotaLimit(create(OrganizationQuotasSchema, { maxMembers: 5 }), 'maxMembers')).toBe(5);
		expect(quotaLimit(create(OrganizationQuotasSchema, {}), 'maxMembers')).toBeUndefined();
		expect(quotaLimit(undefined, 'maxNodes')).toBeUndefined();
	});
});
