import { create } from '@bufbuild/protobuf';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { api } from '#lib/api/client.js';
import { adminApi } from '#lib/api/admin.js';
import { NodeGroupSchema } from '#lib/api/gen/sermon/web/v1/access_pb.js';
import { KeyFiguresSchema, NodeSummarySchema } from '#lib/api/gen/sermon/web/v1/fleet_pb.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { sessionState } from '#lib/stores/session.svelte.js';
import { stepUpState } from '#lib/stores/stepup.svelte.js';
import { fakeSession, stepUpError } from '#lib/test/fakes.js';
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
		vi.spyOn(adminApi.access, 'listNodeGroups').mockResolvedValue({ groups } as never);
	});
	afterEach(() => {
		if (stepUpState.open) stepUpState.finish(false);
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
			.spyOn(adminApi.enrollment, 'createEnrollmentToken')
			.mockResolvedValue({ secret: 'sme_secret' } as never);
		render(AddNode);

		await expect.element(page.getByLabelText('Group')).toHaveValue('g1');
		await page.getByLabelText('Valid for').selectOptions('24 hours');
		await page.getByLabelText('Uses').fill('5');
		await page.getByRole('button', { name: 'Create command' }).click();

		await expect.element(page.getByRole('heading', { name: 'Run on the server' })).toBeVisible();
		expect(document.querySelector('pre')?.textContent).toBe(
			` export SERMON_ENROLLMENT_TOKEN=sme_secret\ncurl -fsSL ${location.origin}/install.sh | sudo --preserve-env=SERMON_ENROLLMENT_TOKEN sh -s -- --server ${location.origin}`
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

	it('builds a command that checks the script for servers without a public certificate', async () => {
		withRights(['*']);
		vi.spyOn(api.fleet, 'getFleetSummary').mockResolvedValue({ nodes: [old] } as never);
		vi.spyOn(adminApi.enrollment, 'createEnrollmentToken').mockResolvedValue({
			secret: 'sme_secret'
		} as never);
		const digest = 'c0ffee'.repeat(10) + 'abcd';
		const realFetch = window.fetch.bind(window);
		const head = vi
			.spyOn(window, 'fetch')
			.mockImplementation((input, init) =>
				input === '/install.sh'
					? Promise.resolve(new Response(null, { headers: { 'X-Sermon-Script-SHA256': digest } }))
					: realFetch(input, init)
			);
		render(AddNode);
		await expect.element(page.getByLabelText('Group')).toHaveValue('g1');
		await page.getByRole('button', { name: 'Create command' }).click();

		await page
			.getByLabelText('The server has no publicly trusted certificate', { exact: false })
			.click();
		const fingerprint = page.getByLabelText('Fingerprint of the server certificate or a CA', {
			exact: true
		});
		await fingerprint.fill('not a fingerprint');
		await expect
			.element(page.getByText('Not a SHA-256 fingerprint', { exact: false }))
			.toBeVisible();
		await expect.element(page.getByRole('button', { name: 'Copy' })).toBeDisabled();

		await fingerprint.fill('SHA256 Fingerprint=' + Array(32).fill('ab').join(':'));
		const pin = 'sha256:' + Array(32).fill('AB').join(':');
		await vi.waitFor(() =>
			expect(document.querySelector('pre')?.textContent).toContain(
				`echo '${digest}  sermon-install.sh' | sha256sum -c -`
			)
		);
		expect(document.querySelector('pre')?.textContent).toBe(
			[
				' export SERMON_ENROLLMENT_TOKEN=sme_secret',
				`curl -fsSLk ${location.origin}/install.sh -o sermon-install.sh`,
				`echo '${digest}  sermon-install.sh' | sha256sum -c - && sudo --preserve-env=SERMON_ENROLLMENT_TOKEN sh sermon-install.sh --server ${location.origin} --ca-fingerprint ${pin}`
			].join('\n')
		);
		expect(head).toHaveBeenCalledWith('/install.sh', expect.objectContaining({ method: 'HEAD' }));
	});

	it('asks for the password before the token is created', async () => {
		withRights(['*']);
		vi.spyOn(api.fleet, 'getFleetSummary').mockResolvedValue({ nodes: [old] } as never);
		const token = vi
			.spyOn(adminApi.enrollment, 'createEnrollmentToken')
			.mockRejectedValueOnce(stepUpError())
			.mockResolvedValueOnce({ secret: 'sme_secret' } as never);
		render(AddNode);

		await expect.element(page.getByLabelText('Group')).toHaveValue('g1');
		await page.getByRole('button', { name: 'Create command' }).click();
		await vi.waitFor(() => expect(stepUpState.open).toBe(true));
		stepUpState.finish(true);

		await expect.element(page.getByRole('heading', { name: 'Run on the server' })).toBeVisible();
		expect(token).toHaveBeenCalledTimes(2);
	});

	it('leads admins to create a group when there is none', async () => {
		withRights(['*']);
		sessionState.current!.orgAdmin = true;
		vi.spyOn(adminApi.access, 'listNodeGroups').mockResolvedValue({ groups: [] } as never);
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
