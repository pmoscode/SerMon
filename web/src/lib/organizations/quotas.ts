import { create } from '@bufbuild/protobuf';
import {
	type OrganizationQuotas,
	OrganizationQuotasSchema
} from '#lib/api/gen/sermon/web/v1/organization_pb.js';
import { m } from '#lib/paraglide/messages.js';

/** The quotas of an organization (M2-32, ADR 0029) with their upper bounds, as on the server. */
export const QUOTA_FIELDS = [
	{ key: 'maxNodes', max: 1_000_000 },
	{ key: 'maxMembers', max: 100_000 },
	{ key: 'retentionCapDays', max: 3650 },
	{ key: 'samplesPerSecond', max: 1_000_000 },
	{ key: 'seriesPerNode', max: 100_000 }
] as const;

export type QuotaKey = (typeof QUOTA_FIELDS)[number]['key'];

/** The form: one text per quota, empty for the instance default. */
export type QuotaForm = Record<QuotaKey, string>;

export function quotaForm(q: OrganizationQuotas | undefined): QuotaForm {
	const form = {} as QuotaForm;
	for (const { key } of QUOTA_FIELDS) {
		const value = q?.[key];
		form[key] = value === undefined ? '' : String(value);
	}
	return form;
}

/** The quotas of the form, or the first field that is no whole number in its range. */
export function quotasFrom(
	form: QuotaForm
): { ok: true; quotas: OrganizationQuotas } | { ok: false; field: QuotaKey } {
	const quotas = create(OrganizationQuotasSchema, {});
	for (const { key, max } of QUOTA_FIELDS) {
		const text = form[key].trim();
		if (text === '') continue;
		if (!/^\d+$/.test(text)) return { ok: false, field: key };
		const value = Number(text);
		if (value < 1 || value > max) return { ok: false, field: key };
		quotas[key] = value;
	}
	return { ok: true, quotas };
}

/** A set quota, undefined for the instance default. */
export function quotaLimit(q: OrganizationQuotas | undefined, key: QuotaKey): number | undefined {
	return q?.[key];
}

/** The label of a quota. */
export function quotaLabel(key: QuotaKey): string {
	switch (key) {
		case 'maxNodes':
			return m.quota_max_nodes();
		case 'maxMembers':
			return m.quota_max_members();
		case 'retentionCapDays':
			return m.quota_retention_cap_days();
		case 'samplesPerSecond':
			return m.quota_samples_per_second();
		case 'seriesPerNode':
			return m.quota_series_per_node();
	}
}
