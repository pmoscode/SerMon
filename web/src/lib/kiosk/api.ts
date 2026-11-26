/**
 * The client of a display (M5-26): its token as the only credential, never the session cookie
 * and without the session's expiry handling (a display has no login to return to).
 */
import { createClient, type Client, type Interceptor } from '@connectrpc/connect';
import { createConnectTransport } from '@connectrpc/connect-web';
import { KioskService } from '#lib/api/gen/sermon/web/v1/kiosk_pb.js';

export type KioskClient = Client<typeof KioskService>;

export function kioskClient(token: string): KioskClient {
	const auth: Interceptor = (next) => (req) => {
		req.header.set('Authorization', `Kiosk ${token}`);
		return next(req);
	};
	return createClient(
		KioskService,
		createConnectTransport({
			baseUrl: '/',
			interceptors: [auth],
			fetch: (input, init) => fetch(input, { ...init, credentials: 'omit' })
		})
	);
}
