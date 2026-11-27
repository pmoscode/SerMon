import { timestampDate } from '@bufbuild/protobuf/wkt';
import type { AgentCA } from '#lib/api/gen/sermon/web/v1/agent_ca_pb.js';

const DAY_MS = 24 * 60 * 60 * 1000;
/** From here the self-monitoring warns, from CRITICAL_DAYS it is critical (selfmonitor, V1-19). */
export const WARNING_DAYS = 90;
export const CRITICAL_DAYS = 30;
/** Agent certificates last 30 days (pki.AgentCertificateLifetime): those of the old CA are gone then. */
export const AGENT_CERTIFICATE_DAYS = 30;

export type Level = 'ok' | 'warning' | 'critical';

/** Where the agent CA stands: the CAs by role and what the superadmin can do now. */
export interface Rollover {
	/** The CA that signs new agent certificates. */
	issuing?: AgentCA;
	/** The CA a running rollover replaces: still trusted, to be retired. */
	previous?: AgentCA;
	retired: AgentCA[];
	/** The first trusted CA's expiry, the whole days left and the level of the warning. */
	expires?: Date;
	daysLeft?: number;
	expired: boolean;
	level: Level;
	/** Only one CA is trusted: a rollover can start. */
	canStart: boolean;
	/** The previous CA has no valid certificates any more. */
	canRetire: boolean;
	/** At the latest then its certificates are replaced or expired. */
	replacedBy?: Date;
}

/** The rollover state of the CAs from GetAgentCAStatus at now (the server checks everything again). */
export function rolloverState(cas: AgentCA[], now: Date): Rollover {
	const trusted = cas.filter((ca) => !ca.retireTime);
	const issuing = trusted.find((ca) => ca.issuing);
	const previous = trusted.find((ca) => !ca.issuing);
	const retired = cas.filter((ca) => ca.retireTime).sort((a, b) => a.id - b.id);
	const ends = trusted
		.map((ca) => (ca.expireTime ? timestampDate(ca.expireTime) : undefined))
		.filter((d): d is Date => d !== undefined)
		.sort((a, b) => a.getTime() - b.getTime());
	const expires = ends[0];
	const left = expires ? expires.getTime() - now.getTime() : Infinity;
	const level: Level =
		left <= CRITICAL_DAYS * DAY_MS ? 'critical' : left <= WARNING_DAYS * DAY_MS ? 'warning' : 'ok';
	const started = previous && issuing?.createTime ? timestampDate(issuing.createTime) : undefined;
	return {
		issuing,
		previous,
		retired,
		expires,
		daysLeft: expires ? Math.max(0, Math.floor(left / DAY_MS)) : undefined,
		expired: left <= 0,
		level,
		canStart: trusted.length === 1 && issuing !== undefined,
		canRetire: previous !== undefined && previous.certificatesInUse === 0n,
		replacedBy: started ? new Date(started.getTime() + AGENT_CERTIFICATE_DAYS * DAY_MS) : undefined
	};
}
