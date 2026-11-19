import { create, fromBinary, toBinary } from '@bufbuild/protobuf';
import { MethodOptions_IdempotencyLevel } from '@bufbuild/protobuf/wkt';
import { describe, expect, it } from 'vitest';
import { GetVersionResponseSchema, SystemService } from './gen/sermon/web/v1/system_pb.js';

describe('generated web API (sermon.web.v1)', () => {
	it('roundtrips messages through the wire format', () => {
		const original = create(GetVersionResponseSchema, { version: '1.4.2' });

		const decoded = fromBinary(
			GetVersionResponseSchema,
			toBinary(GetVersionResponseSchema, original)
		);

		expect(decoded.version).toBe('1.4.2');
	});

	it('describes SystemService.GetVersion as side-effect free (Connect GET allowed)', () => {
		expect(SystemService.typeName).toBe('sermon.web.v1.SystemService');
		expect(SystemService.method.getVersion.idempotency).toBe(
			MethodOptions_IdempotencyLevel.NO_SIDE_EFFECTS
		);
	});
});
