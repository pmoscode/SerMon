import { create } from '@bufbuild/protobuf';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { api } from '#lib/api/client.js';
import { NodeSchema, SetNodeTagsResponseSchema } from '#lib/api/gen/sermon/web/v1/node_pb.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { sessionState } from '#lib/stores/session.svelte.js';
import { stepUpState } from '#lib/stores/stepup.svelte.js';
import { fakeSession, stepUpError } from '#lib/test/fakes.js';
import NodeTags from './NodeTags.svelte';

const group = '0193f5a0-0000-7000-8000-0000000000c1';
const nodeId = '0193f5a0-0000-7000-8000-0000000000d1';
const node = create(NodeSchema, {
	id: nodeId,
	hostname: 'web-01',
	groupId: group,
	tags: { role: 'web', env: 'prod' }
});

function rights(groups: string[]) {
	sessionState.current = { ...fakeSession(), tagGroupIds: groups };
	sessionState.status = 'authenticated';
}

describe('NodeTags', () => {
	beforeEach(() => switchLocale('en'));
	afterEach(() => {
		vi.restoreAllMocks();
		if (stepUpState.open) stepUpState.finish(false);
		sessionState.current = undefined;
		sessionState.status = 'unknown';
	});

	it('shows the tags to everyone, editing only to admins of the group', async () => {
		rights(['0193f5a0-0000-7000-8000-0000000000ff']);
		render(NodeTags, { node, onchange: () => {} });
		const list = page.getByRole('list', { name: 'Tags' });
		await expect.element(list).toMatchTextContent(/env=prod\s*role=web/);
		expect(page.getByRole('button').elements()).toHaveLength(0);
	});

	it('names a bad pair and sends nothing', async () => {
		rights([group]);
		const set = vi.spyOn(api.node, 'setNodeTags');
		render(NodeTags, { node, onchange: () => {} });
		await page.getByRole('button', { name: 'Edit tags' }).click();
		await page.getByLabelText('New tags').fill('env=prod Role=db');
		await page.getByRole('button', { name: 'Save' }).click();
		await expect.element(page.getByRole('alert')).toMatchTextContent(/Role=db/);
		expect(set).not.toHaveBeenCalled();
	});

	it('saves after the step-up and reloads', async () => {
		rights(['*']);
		const set = vi
			.spyOn(api.node, 'setNodeTags')
			.mockRejectedValueOnce(stepUpError())
			.mockResolvedValueOnce(create(SetNodeTagsResponseSchema, {}));
		const changed = vi.fn();
		render(NodeTags, { node, onchange: changed });
		await page.getByRole('button', { name: 'Edit tags' }).click();
		await expect.element(page.getByLabelText('New tags')).toHaveValue('env=prod, role=web');
		await page.getByLabelText('New tags').fill('env=staging, site=fra-1');
		await page.getByRole('button', { name: 'Save' }).click();
		await vi.waitFor(() => expect(stepUpState.open).toBe(true));
		stepUpState.finish(true);
		await vi.waitFor(() => expect(set).toHaveBeenCalledTimes(2));
		expect(set.mock.calls[1][0]).toEqual({ nodeId, tags: { env: 'staging', site: 'fra-1' } });
		await vi.waitFor(() => expect(changed).toHaveBeenCalledOnce());
	});
});
