import {
	api,
	Code,
	codeOf,
	onSecondFactorSetupRequired,
	onSessionExpired
} from '#lib/api/client.js';
import type { Session } from '#lib/api/gen/sermon/web/v1/auth_pb.js';

/** unknown: not checked yet; anonymous: not logged in; expired: the session ended while using the app. */
export type SessionStatus = 'unknown' | 'anonymous' | 'authenticated' | 'expired';

/** Why a login failed, for the message shown. */
export type LoginFailure = 'credentials' | 'rate_limited' | 'unavailable';

/** Why a password or code confirmation failed. */
export type ConfirmFailure = 'wrong_password' | 'wrong_code' | 'rate_limited' | 'unavailable';

/** Why the second step of a login failed; expired: start again with the password. */
export type SecondFactorFailure = 'wrong_code' | 'rate_limited' | 'expired' | 'unavailable';

class SessionState {
	status = $state<SessionStatus>('unknown');
	current = $state<Session | undefined>(undefined);

	constructor() {
		onSessionExpired(() => this.expire());
		onSecondFactorSetupRequired(() => {
			if (this.current) this.current = { ...this.current, secondFactorSetupRequired: true };
		});
	}

	/** True while the organization requires a second factor the user hasn't set up. */
	get secondFactorSetupRequired(): boolean {
		return this.current?.secondFactorSetupRequired ?? false;
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

	/**
	 * Logs in; returns the reason on failure or "second_factor" when the code is needed next
	 * (verifySecondFactor). Messages never tell which part was wrong.
	 */
	async login(
		email: string,
		password: string
	): Promise<LoginFailure | 'second_factor' | undefined> {
		try {
			const res = await api.auth.login({ email, password });
			if (res.secondFactorRequired) return 'second_factor';
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

	/** Second step of a login: the code from the app or a recovery code. */
	async verifySecondFactor(code: string): Promise<SecondFactorFailure | undefined> {
		try {
			const res = await api.auth.verifySecondFactor({ code });
			this.current = res.session;
			this.status = 'authenticated';
			return undefined;
		} catch (error) {
			switch (codeOf(error)) {
				case Code.PermissionDenied:
				case Code.InvalidArgument:
					return 'wrong_code';
				case Code.ResourceExhausted:
					return 'rate_limited';
				case Code.Unauthenticated:
				case Code.FailedPrecondition:
					return 'expired';
				default:
					return 'unavailable';
			}
		}
	}

	/** Confirms a code for sensitive actions (step-up of users with a second factor). */
	async confirmSecondFactor(code: string): Promise<ConfirmFailure | undefined> {
		try {
			const res = await api.auth.confirmSecondFactor({ code });
			this.current = res.session;
			return undefined;
		} catch (error) {
			return this.confirmFailure(error, 'wrong_code');
		}
	}

	/** Confirms the password for sensitive actions (step-up, KONZEPT §7). */
	async confirmPassword(password: string): Promise<ConfirmFailure | undefined> {
		try {
			const res = await api.auth.confirmPassword({ password });
			this.current = res.session;
			return undefined;
		} catch (error) {
			return this.confirmFailure(error, 'wrong_password');
		}
	}

	private confirmFailure(error: unknown, wrong: ConfirmFailure): ConfirmFailure {
		switch (codeOf(error)) {
			case Code.PermissionDenied:
			case Code.InvalidArgument:
				return wrong;
			case Code.ResourceExhausted:
				return 'rate_limited';
			case Code.Unauthenticated:
				this.expire();
				return 'unavailable';
			default:
				return 'unavailable';
		}
	}

	/** Reloads the session view (e.g. after the second factor was set up or disabled). */
	async refresh(): Promise<void> {
		const res = await api.auth.getSession({});
		this.current = res.session;
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
