import { create } from '@bufbuild/protobuf';
import { Code, ConnectError } from '@connectrpc/connect';
import {
	SessionSchema,
	StepUpRequiredSchema,
	type Session
} from '#lib/api/gen/sermon/web/v1/auth_pb.js';

/** A password that meets the policy; for tests only. */
export const PASSPHRASE = 'a long passphrase'; // betterleaks:allow

/** A Connect error as the server sends it. */
export function rpcError(code: Code): ConnectError {
	return new ConnectError('fake', code);
}

/** A session of Mia with the given organizations; the first one is active. */
export function fakeSession(...organizations: string[]): Session {
	const orgs = (organizations.length ? organizations : ['Alpha']).map((name, i) => ({
		id: `0193f5a0-0000-7000-8000-00000000000${i}`,
		name
	}));
	return create(SessionSchema, {
		userId: '0193f5a0-0000-7000-8000-0000000000ff',
		email: 'mia@example.org',
		displayName: 'Mia',
		activeOrganizationId: orgs[0].id,
		organizations: orgs
	});
}

/** The server's answer to a sensitive action without a recent re-authentication. */
export function stepUpError(): ConnectError {
	return new ConnectError(
		'please confirm your identity again',
		Code.FailedPrecondition,
		undefined,
		[{ desc: StepUpRequiredSchema, value: {} }]
	);
}
