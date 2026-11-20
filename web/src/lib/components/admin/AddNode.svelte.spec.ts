import { create } from '@bufbuild/protobuf';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { api } from '#lib/api/client.js';
import { NodeGroupSchema } from '#lib/api/gen/sermon/web/v1/access_pb.js';
import { KeyFiguresSchema, NodeSummarySchema } from '#lib/api/gen/sermon/web/v1/fleet_pb.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { sessionState } from '#lib/stores/session.svelte.js';
import { fakeSession } from '#lib/test/fakes.js';
import AddNode from './AddNode.svelte';

const groups = [
	create(NodeGroupSchema, { id: 'g1', name: 'web' }),
	create(NodeGroupSchema, { id: 'g2', name: 'db' })
];
const old = create(NodeSummarySchema, { nodeId: 'n0', hostname: 'web-00', online: true });

function withRights(enrollGroupIds: string[]) {
	const s = fakeSession();
	s.enrollGroupIds = enrollGroupIds;
	sessionState.current = s;
}

describe('AddNode', () => {
	beforeEach(() => {
		switchLocale('en');
		vi.spyOn(api.access, 'listNodeGroups').mockResolvedValue({ groups } as never);
	});
	afterEach(() => {
		vi.restoreAllMocks();
		sessionState.current = undefined;
	});

	it('offers only the groups the caller may enroll into', async () => {
		withRights(['g2']);
		render(AddNode);

		const group = page.getByLabelText('Group');
		await expect.element(group).toHaveValue('g2');
		expect(group.element().querySelectorAll('option')).toHaveLength(1);
	});

	it('shows the command and follows the new node until it delivers', async () => {
		withRights(['*']);
		const fresh = create(NodeSummarySchema, { nodeId: 'n1', hostname: 'web-01', online: true });
		const summary = vi
			.spyOn(api.fleet, 'getFleetSummary')
			.mockResolvedValueOnce({ nodes: [old] } as never)
			.mockResolvedValueOnce({ nodes: [old, fresh] } as never)
			.mockResolvedValue({
				nodes: [old, { ...fresh, figures: create(KeyFiguresSchema, { cpuBusyRatio: 0.1 }) }]
			} as never);
		const token = vi
			.spyOn(api.enrollment, 'createEnrollmentToken')
			.mockResolvedValue({ secret: 'sme_secret' } as never);
		render(AddNode);

		await expect.element(page.getByLabelText('Group')).toHaveValue('g1');
		await page.getByLabelText('Valid for').selectOptions('24 hours');
		await page.getByLabelText('Uses').fill('5');
		await page.getByRole('button', { name: 'Create command' }).click();

		await expect.element(page.getByRole('heading', { name: 'Run on the server' })).toBeVisible();
		expect(document.querySelector('pre')?.textContent).toBe(
			`curl -fsSL ${location.origin}/install.sh | sudo SERMON_ENROLLMENT_TOKEN=sme_secret sh -s -- --server ${location.origin}`
		);
		expect(token.mock.calls[0][0]).toEqual({ groupId: 'g1', ttlSeconds: 86400, maxUses: 5 });
		expect(summary.mock.calls[0][0]).toEqual({ groupId: 'g1' });
		await expect.element(page.getByText('Waiting for the connection …')).toBeVisible();

		await expect.element(page.getByText('Connected: web-01'), { timeout: 5000 }).toBeVisible();
		await expect.element(page.getByText('First metrics received'), { timeout: 5000 }).toBeVisible();
		await expect
			.element(page.getByRole('link', { name: 'Open node' }))
			.toHaveAttribute('href', '/nodes/n1');
	});

	it('leads admins to create a group when there is none', async () => {
		withRights(['*']);
		sessionState.current!.orgAdmin = true;
		vi.spyOn(api.access, 'listNodeGroups').mockResolvedValue({ groups: [] } as never);
		render(AddNode);

		await expect
			.element(
				page.getByText('Nodes always belong to a group, and there is none yet.', { exact: false })
			)
			.toBeVisible();
		await expect
			.element(page.getByRole('link', { name: 'Create a group' }))
			.toHaveAttribute('href', '/admin');
		await expect
			.element(page.getByRole('button', { name: 'Create command' }))
			.not.toBeInTheDocument();
	});

	it('explains missing rights', async () => {
		withRights([]);
		render(AddNode);

		await expect
			.element(page.getByRole('alert'))
			.toHaveTextContent('Operators and admins of a group may add nodes.');
	});
});
