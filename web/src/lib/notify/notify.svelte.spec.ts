import { ConnectError } from '@connectrpc/connect';
import { beforeEach, describe, expect, it } from 'vitest';
import { Code } from '#lib/api/client.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { m } from '#lib/paraglide/messages.js';
import { notifyError } from './notify.js';

describe('notifyError', () => {
	beforeEach(() => switchLocale('en'));

	// RESOURCE_EXHAUSTED is either the channel limit of the organization (M2-38) or too many tests.
	it('tells the channel limit from too many tests', () => {
		const limit = new ConnectError(
			'at most 50 notification channels per organization',
			Code.ResourceExhausted
		);
		expect(notifyError(limit)).toBe(m.notify_error_too_many_channels());
		const tests = new ConnectError(
			'too many test notifications, please wait a minute',
			Code.ResourceExhausted
		);
		expect(notifyError(tests)).toBe(m.notify_error_too_many_tests());
	});
});
