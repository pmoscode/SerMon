import { timestampFromMs } from '@bufbuild/protobuf/wkt';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { sessionState } from '#lib/stores/session.svelte.js';
import { fakeSession } from '#lib/test/fakes.js';
import InstanceHealth from './InstanceHealth.svelte';

const problems = [
	{ check: 'ingest_lag', level: 'warning', since: timestampFromMs(Date.now()), value: 75 },
	{ check: 'database_size', level: 'critical', since: timestampFromMs(Date.now()), value: 0.93 },
	{ check: 'dropped_samples', level: 'warning', since: timestampFromMs(Date.now()), value: 30 },
	{
		check: 'dropped_security_events',
		level: 'warning',
		since: timestampFromMs(Date.now()),
		value: 4
	},
	{ check: 'logs_unavailable', level: 'warning', since: timestampFromMs(Date.now()), value: 0 }
];

describe('InstanceHealth', () => {
	beforeEach(() => switchLocale('en'));
	afterEach(() => {
		sessionState.current = undefined;
	});

	it('tells superadmins about the problems of the server in words', async () => {
		sessionState.current = {
			...fakeSession(),
			superadmin: true,
			instanceProblems: problems
		} as never;
		render(InstanceHealth);
		const bar = page.getByRole('status');
		await expect.element(bar).toMatchTextContent(/Agent data arrives late \(75 s\)/);
		await expect.element(bar).toMatchTextContent(/The database is filling up \(93 %\)/);
		await expect.element(bar).toMatchTextContent(/30 samples dropped/);
		await expect
			.element(bar)
			.toMatchTextContent(/4 security events \(logins, sudo, accounts\) dropped/);
		await expect.element(bar).toMatchTextContent(/Logs can't be stored \(Loki unavailable\)/);
		await expect.element(bar).toMatchTextContent(/Critical/);
	});

	it('shows nothing to members of an organization', async () => {
		sessionState.current = {
			...fakeSession(),
			superadmin: false,
			instanceProblems: problems
		} as never;
		render(InstanceHealth);
		expect(page.getByRole('status').elements()).toHaveLength(0);
	});

	it('shows nothing while all is well', async () => {
		sessionState.current = { ...fakeSession(), superadmin: true, instanceProblems: [] } as never;
		render(InstanceHealth);
		expect(page.getByRole('status').elements()).toHaveLength(0);
	});
});
