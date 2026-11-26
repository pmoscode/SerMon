import { create } from '@bufbuild/protobuf';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { adminApi } from '#lib/api/admin.js';
import { ListAuditLogResponseSchema } from '#lib/api/gen/sermon/web/v1/access_pb.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import AuditPanel from './AuditPanel.svelte';

describe('AuditPanel', () => {
	beforeEach(() => switchLocale('en'));
	afterEach(() => vi.restoreAllMocks());

	// Review M4-30: details carry free text (a reboot reason); a bidi override must not make an
	// entry read differently from what it stores.
	it('shows target and details with their controls visible', async () => {
		vi.spyOn(adminApi.access, 'listAuditLog').mockResolvedValue(
			create(ListAuditLogResponseSchema, {
				entries: [
					{
						seq: 1n,
						action: 'nodes.reboot',
						target: 'node:\u202ex',
						details: { reason: 'kernel\u202eetadpu', nodes: 'web-01' }
					}
				]
			})
		);
		render(AuditPanel, { members: [] });
		const row = page.getByRole('row').nth(1);
		await expect.element(row).toBeVisible();
		const text = row.element().textContent ?? '';
		expect(text).toContain('reason=kernel⟨U+202E⟩etadpu');
		expect(text).toContain('node:⟨U+202E⟩x');
		expect(text).toContain('nodes=web-01');
	});
});
