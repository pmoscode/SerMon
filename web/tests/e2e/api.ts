import type { Page, Route } from '@playwright/test';

/**
 * Fakes the web API (Connect JSON) for E2E tests: the test server runs without a database.
 * Handlers are keyed by "Service/Method"; unknown calls answer UNIMPLEMENTED.
 */
export type Handler = (body: unknown) => { status?: number; json: unknown };

export const unauthenticated = {
	status: 401,
	json: { code: 'unauthenticated', message: 'no session' }
};

export const session = {
	userId: '0193f5a0-0000-7000-8000-0000000000ff',
	email: 'mia@example.org',
	displayName: 'Mia Muster',
	activeOrganizationId: '0193f5a0-0000-7000-8000-00000000000a',
	organizations: [
		{ id: '0193f5a0-0000-7000-8000-00000000000a', name: 'Alpha GmbH' },
		{ id: '0193f5a0-0000-7000-8000-00000000000b', name: 'Beta AG' }
	],
	hasPassword: true
};

export async function mockApi(page: Page, handlers: Record<string, Handler>): Promise<string[]> {
	const calls: string[] = [];
	await page.route(/\/sermon\.web\.v1\.(\w+)\/(\w+)$/, async (route: Route) => {
		const [, service, method] = /sermon\.web\.v1\.(\w+)\/(\w+)$/.exec(route.request().url()) ?? [];
		const key = `${service}/${method}`;
		calls.push(key);
		const handler = handlers[key];
		const { status = 200, json } = handler
			? handler(route.request().postDataJSON())
			: { status: 501, json: { code: 'unimplemented', message: key } };
		await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(json) });
	});
	return calls;
}

/** An instance that is set up, with or without a valid session cookie, and without nodes. */
export function installed(loggedIn: boolean): Record<string, Handler> {
	return {
		'SetupService/GetSetupStatus': () => ({ json: { setupRequired: false } }),
		'AuthService/GetSession': () => (loggedIn ? { json: { session } } : unauthenticated),
		// Without an OIDC provider (M3-22); tests with one replace it.
		'AuthService/GetLoginOptions': () => ({ json: {} }),
		'AuthService/ListOidcIdentities': () => ({ json: {} }),
		'PasskeyService/ListPasskeys': () => ({ json: {} }),
		'FleetService/GetFleetSummary': () => ({ json: {} }),
		// The anomaly band of the node pages (M4-25): none, the node learnt.
		'AnomalyService/GetNodeAnomalies': () => ({ json: {} })
	};
}

/** A Connect stream: enveloped JSON messages, then the end of the stream. */
export function envelopes(...messages: unknown[]): Buffer {
	const frame = (flags: number, json: unknown) => {
		const body = Buffer.from(JSON.stringify(json));
		const head = Buffer.alloc(5);
		head.writeUInt8(flags, 0);
		head.writeUInt32BE(body.length, 1);
		return Buffer.concat([head, body]);
	};
	return Buffer.concat([...messages.map((m) => frame(0, m)), frame(2, {})]);
}
