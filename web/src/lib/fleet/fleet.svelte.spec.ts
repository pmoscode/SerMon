import { create } from '@bufbuild/protobuf';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { api, Code } from '#lib/api/client.js';
import {
	GetFleetSummaryResponseSchema,
	NodeSummarySchema
} from '#lib/api/gen/sermon/web/v1/fleet_pb.js';
import { rpcError } from '#lib/test/fakes.js';
import { FleetState } from './fleet.svelte.js';

const summary = create(GetFleetSummaryResponseSchema, {
	total: 2,
	online: 1,
	offline: 1,
	nodes: [
		create(NodeSummarySchema, { nodeId: 'n1', hostname: 'web-01', online: true }),
		create(NodeSummarySchema, { nodeId: 'n2', hostname: 'db-01', online: false })
	]
});

describe('FleetState', () => {
	afterEach(() => vi.restoreAllMocks());

	it('loads the summary and the CPU sparklines of the online nodes', async () => {
		vi.spyOn(api.fleet, 'getFleetSummary').mockResolvedValue(summary);
		const query = vi.spyOn(api.metrics, 'queryMetrics').mockResolvedValue({
			series: [{ nodeId: 'n1', points: [{ avg: 900_000 }, { avg: 250_000 }] }]
		} as never);
		const fleet = new FleetState();

		await fleet.load();
		await fleet.loadSparklines(Date.UTC(2026, 10, 20, 12));

		expect(fleet.status).toBe('ready');
		expect(fleet.summary?.nodes).toHaveLength(2);
		const request = query.mock.calls[0][0];
		expect(request.selector?.nodeIds).toEqual(['n1']);
		expect(request.selector?.labels).toEqual({ mode: 'idle' });
		expect(Number(request.to?.seconds) - Number(request.from?.seconds)).toBe(900);
		expect(fleet.sparklines.n1[0]).toBeCloseTo(0.1);
		expect(fleet.sparklines.n1[1]).toBeCloseTo(0.75);
	});

	it('shows an error first and keeps older data later', async () => {
		const get = vi
			.spyOn(api.fleet, 'getFleetSummary')
			.mockRejectedValueOnce(rpcError(Code.Unavailable));
		const fleet = new FleetState();
		await fleet.load();
		expect(fleet.status).toBe('error');

		get.mockResolvedValueOnce(summary).mockRejectedValueOnce(rpcError(Code.Unavailable));
		await fleet.load();
		await fleet.load();
		expect(fleet.status).toBe('ready');
		expect(fleet.stale).toBe(true);
		expect(fleet.summary?.nodes).toHaveLength(2);
	});

	it('leaves an ended session to the login redirect', async () => {
		vi.spyOn(api.fleet, 'getFleetSummary').mockRejectedValue(rpcError(Code.Unauthenticated));
		const fleet = new FleetState();
		await fleet.load();
		expect(fleet.status).toBe('loading');
	});
});
