import { create } from '@bufbuild/protobuf';
import { beforeEach, describe, expect, it } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { NodeSchema, SecuritySource } from '#lib/api/gen/sermon/web/v1/node_pb.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import SecuritySourceNotice from './SecuritySourceNotice.svelte';

function node(securitySource: SecuritySource) {
	return create(NodeSchema, {
		id: '0193f5a0-0000-7000-8000-0000000000d1',
		hostname: 'alp-01',
		securitySource
	});
}

describe('SecuritySourceNotice', () => {
	beforeEach(() => switchLocale('en'));

	// M2-31: without journald and a readable log file the node sends no security events.
	it('warns when the node has no source, with the ways to fix it', async () => {
		render(SecuritySourceNotice, { node: node(SecuritySource.NONE) });
		const notice = page.getByRole('status');
		await expect.element(notice).toMatchTextContent(/No security events/);
		await expect.element(notice).toMatchTextContent(/--grant-log-access/);
		await expect.element(notice).toMatchTextContent(/rsyslog/);
		await expect.element(page.getByRole('button', { name: /help/i })).toBeVisible();
	});

	it('notes events from a log file as unverified', async () => {
		render(SecuritySourceNotice, { node: node(SecuritySource.FILE_UNVERIFIED) });
		await expect.element(page.getByRole('status')).toMatchTextContent(/not verified/);
	});

	it.each([SecuritySource.JOURNAL, SecuritySource.FILE_VERIFIED, SecuritySource.UNSPECIFIED])(
		'says nothing for the journal, the rsyslog template or an older agent (%s)',
		async (source) => {
			render(SecuritySourceNotice, { node: node(source) });
			await expect.element(page.getByRole('status')).not.toBeInTheDocument();
		}
	);
});
