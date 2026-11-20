/**
 * Connect clients of the web API (sermon.web.v1). The session is an HttpOnly cookie that the
 * browser sends itself – no token ever reaches JavaScript (KONZEPT §18.3).
 */
import {
	Code,
	ConnectError,
	createClient,
	type Client,
	type Interceptor
} from '@connectrpc/connect';
import { createConnectTransport } from '@connectrpc/connect-web';
import type { DescService } from '@bufbuild/protobuf';
import { AccessService } from './gen/sermon/web/v1/access_pb.js';
import { AuthService, SecondFactorSetupRequiredSchema } from './gen/sermon/web/v1/auth_pb.js';
import { EnrollmentTokenService } from './gen/sermon/web/v1/enrollment_token_pb.js';
import { FleetService } from './gen/sermon/web/v1/fleet_pb.js';
import { MetricsService } from './gen/sermon/web/v1/metrics_pb.js';
import { NodeService } from './gen/sermon/web/v1/node_pb.js';
import { SetupService } from './gen/sermon/web/v1/setup_pb.js';
import { SecurityPolicyService, TwoFactorService } from './gen/sermon/web/v1/two_factor_pb.js';

let onExpired: () => void = () => {};

/** Called when a call fails because the session ended (idle or absolute timeout, logout elsewhere). */
export function onSessionExpired(handler: () => void): void {
	onExpired = handler;
}

let onSetupRequired: () => void = () => {};

/** Called when the organization requires a second factor the user hasn't set up yet. */
export function onSecondFactorSetupRequired(handler: () => void): void {
	onSetupRequired = handler;
}

/** Whether an error asks for the second-factor setup (FAILED_PRECONDITION with the detail). */
export function needsSecondFactorSetup(error: unknown): boolean {
	const err = ConnectError.from(error);
	return (
		err.code === Code.FailedPrecondition &&
		err.findDetails(SecondFactorSetupRequiredSchema).length > 0
	);
}

/** Reports UNAUTHENTICATED answers of services that need a session, and a missing second factor. */
export const expiryInterceptor: Interceptor = (next) => async (request) => {
	try {
		return await next(request);
	} catch (error) {
		const anonymous = [AuthService.typeName, SetupService.typeName];
		if (
			ConnectError.from(error).code === Code.Unauthenticated &&
			!anonymous.includes(request.service.typeName)
		) {
			onExpired();
		} else if (needsSecondFactorSetup(error)) {
			onSetupRequired();
		}
		throw error;
	}
};

const transport = createConnectTransport({
	baseUrl: '/',
	interceptors: [expiryInterceptor],
	fetch: (input, init) => fetch(input, { ...init, credentials: 'same-origin' })
});

function client<T extends DescService>(service: T): Client<T> {
	return createClient(service, transport);
}

/** The clients; tests replace single services with fakes. */
export const api = {
	auth: client(AuthService),
	setup: client(SetupService),
	access: client(AccessService),
	enrollment: client(EnrollmentTokenService),
	fleet: client(FleetService),
	metrics: client(MetricsService),
	node: client(NodeService),
	twoFactor: client(TwoFactorService),
	securityPolicy: client(SecurityPolicyService)
};

/** The Connect code of an error (Unknown for anything else). */
export function codeOf(error: unknown): Code {
	return ConnectError.from(error).code;
}

export { Code };
