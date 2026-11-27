import { create } from '@bufbuild/protobuf';
import { timestampFromDate } from '@bufbuild/protobuf/wkt';
import { describe, expect, it } from 'vitest';
import { type AgentCA, AgentCASchema } from '#lib/api/gen/sermon/web/v1/agent_ca_pb.js';
import { rolloverState } from './rollover.js';

const day = 24 * 60 * 60 * 1000;
const now = new Date('2028-09-01T12:00:00Z');

function ca(fields: {
	id: number;
	issuing?: boolean;
	created: Date;
	expires: Date;
	retired?: Date;
	inUse?: number;
}): AgentCA {
	return create(AgentCASchema, {
		id: fields.id,
		issuing: fields.issuing ?? false,
		createTime: timestampFromDate(fields.created),
		expireTime: timestampFromDate(fields.expires),
		retireTime: fields.retired ? timestampFromDate(fields.retired) : undefined,
		rootFingerprint: 'ab'.repeat(32),
		certificatesInUse: BigInt(fields.inUse ?? 0)
	});
}

describe('rolloverState', () => {
	// V1-19: one trusted CA – a rollover can start; the level follows the self-monitoring.
	it('offers to start a rollover and warns from 90 days before the expiry', () => {
		const at = (left: number) =>
			rolloverState(
				[
					ca({
						id: 1,
						issuing: true,
						created: new Date(now.getTime() - 640 * day),
						expires: new Date(now.getTime() + left)
					})
				],
				now
			);
		expect(at(90 * day + 1000)).toMatchObject({
			level: 'ok',
			daysLeft: 90,
			canStart: true,
			canRetire: false
		});
		expect(at(90 * day)).toMatchObject({ level: 'warning', daysLeft: 90 });
		expect(at(30 * day)).toMatchObject({ level: 'critical', daysLeft: 30 });
		expect(at(-day)).toMatchObject({ level: 'critical', daysLeft: 0, expired: true });
	});

	// During a rollover the old CA expires first and must be retired before; it can be once its
	// certificates are gone, at the latest 30 days after the start.
	it('during a rollover watches the previous CA and lets it go without certificates', () => {
		const started = new Date(now.getTime() - 10 * day);
		const old = ca({
			id: 1,
			created: new Date(now.getTime() - 700 * day),
			expires: new Date(now.getTime() + 30 * day),
			inUse: 4
		});
		const current = ca({
			id: 2,
			issuing: true,
			created: started,
			expires: new Date(started.getTime() + 730 * day)
		});
		const retired = ca({
			id: 0,
			created: new Date(0),
			expires: new Date(730 * day),
			retired: new Date(700 * day)
		});
		const state = rolloverState([current, retired, old], now);
		expect(state.issuing?.id).toBe(2);
		expect(state.previous?.id).toBe(1);
		expect(state.retired.map((c) => c.id)).toEqual([0]);
		expect(state).toMatchObject({
			canStart: false,
			canRetire: false,
			level: 'critical',
			daysLeft: 30
		});
		expect(state.replacedBy?.toISOString()).toBe(
			new Date(started.getTime() + 30 * day).toISOString()
		);

		const empty = ca({
			id: 1,
			created: new Date(0),
			expires: new Date(now.getTime() + 30 * day),
			inUse: 0
		});
		expect(rolloverState([current, empty], now).canRetire).toBe(true);
	});

	it('knows nothing without a CA', () => {
		expect(rolloverState([], now)).toMatchObject({
			level: 'ok',
			canStart: false,
			canRetire: false
		});
	});
});
