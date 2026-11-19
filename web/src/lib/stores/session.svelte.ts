import { api, Code, codeOf, onSessionExpired } from '#lib/api/client.js';
import type { Session } from '#lib/api/gen/sermon/web/v1/auth_pb.js';

/** unknown: not checked yet; anonymous: not logged in; expired: the session ended while using the app. */
export type SessionStatus = 'unknown' | 'anonymous' | 'authenticated' | 'expired';

/** Why a login failed, for the message shown. */
export type LoginFailure = 'credentials' | 'rate_limited' | 'unavailable';

class SessionState {
	status = $state<SessionStatus>('unknown');
	current = $state<Session | undefined>(undefined);

	constructor() {
		onSessionExpired(() => this.expire());
	}

	/** The organization the session acts in. */
	get organization() {
		return this.current?.organizations.find((o) => o.id === this.current?.activeOrganizationId);
	}

	/** Checks the cookie session with the server. */
	async load(): Promise<SessionStatus> {
		try {
			const res = await api.auth.getSession({});
			this.current = res.session;
			this.status = 'authenticated';
		} catch (error) {
			if (codeOf(error) !== Code.Unauthenticated) throw error;
			this.current = undefined;
			this.status = this.status === 'expired' ? 'expired' : 'anonymous';
		}
		return this.status;
	}

	/** Logs in; returns the reason on failure. Messages never tell which part was wrong. */
	async login(email: string, password: string): Promise<LoginFailure | undefined> {
		try {
			const res = await api.auth.login({ email, password });
			this.current = res.session;
			this.status = 'authenticated';
			return undefined;
		} catch (error) {
			switch (codeOf(error)) {
				case Code.Unauthenticated:
				case Code.InvalidArgument:
					return 'credentials';
				case Code.ResourceExhausted:
					return 'rate_limited';
				default:
					return 'unavailable';
			}
		}
	}

	async logout(): Promise<void> {
		try {
			await api.auth.logout({});
		} finally {
			this.current = undefined;
			this.status = 'anonymous';
		}
	}

	/** Acts in another organization of the user (new session cookie, KONZEPT §7). */
	async switchOrganization(id: string): Promise<void> {
		const res = await api.auth.switchOrganization({ organizationId: id });
		this.current = res.session;
	}

	expire(): void {
		this.current = undefined;
		this.status = 'expired';
	}
}

export const sessionState = new SessionState();
