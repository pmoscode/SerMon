import { beforeEach, describe, expect, it, vi } from 'vitest';
import { page, userEvent } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import NodePicker from './NodePicker.svelte';

const id = (n: number) => `0193f5a0-0000-7000-8000-00000000000${n}`;
const options = [1, 2, 3, 4, 5].map((n) => ({ nodeId: id(n), hostname: `web-0${n}` }));

describe('NodePicker', () => {
	beforeEach(() => switchLocale('en'));

	it('adds a node from the list and offers only those not chosen', async () => {
		const onadd = vi.fn();
		render(NodePicker, { options, selected: [id(1)], onadd, onremove: () => {} });
		const select = page.getByRole('combobox', { name: 'Add node' });
		const offered = [...(select.element() as HTMLSelectElement).options].map((o) => o.text);
		expect(offered).not.toContain('web-01');
		expect(offered).toContain('web-02');
		await userEvent.selectOptions(select, 'web-03');
		expect(onadd).toHaveBeenCalledWith(id(3));
		await expect.element(page.getByText('Choose at least 2 nodes to compare.')).toBeVisible();
	});

	it('removes a chosen node', async () => {
		const onremove = vi.fn();
		render(NodePicker, { options, selected: [id(1), id(2)], onadd: () => {}, onremove });
		const chosen = page.getByRole('list', { name: 'Compared nodes' });
		expect(chosen.element().textContent).toContain('web-01');
		await page.getByRole('button', { name: 'Remove web-02' }).click();
		expect(onremove).toHaveBeenCalledWith(id(2));
		expect(page.getByText('Choose at least 2 nodes to compare.').elements()).toHaveLength(0);
	});

	it('takes no fifth node', async () => {
		render(NodePicker, {
			options,
			selected: [1, 2, 3, 4].map(id),
			onadd: () => {},
			onremove: () => {}
		});
		await expect.element(page.getByRole('combobox', { name: 'Add node' })).toBeDisabled();
		await expect.element(page.getByText('At most 4 nodes can be compared.')).toBeVisible();
	});

	it('names nodes it does not know (deleted or of another organization)', async () => {
		const onremove = vi.fn();
		const foreign = '0193f5a0-0000-7000-8000-0000000000ff';
		render(NodePicker, { options, selected: [id(1), foreign], onadd: () => {}, onremove });
		await page.getByRole('button', { name: 'Remove Unknown node' }).click();
		expect(onremove).toHaveBeenCalledWith(foreign);
	});

	it('shows hostnames as text only', async () => {
		const hostile = '<img src=x onerror=alert(1)>';
		render(NodePicker, {
			options: [{ nodeId: id(1), hostname: hostile }],
			selected: [id(1)],
			onadd: () => {},
			onremove: () => {}
		});
		const chosen = page.getByRole('list', { name: 'Compared nodes' });
		expect(chosen.element().textContent).toContain(hostile);
		expect(chosen.element().querySelector('img')).toBeNull();
	});
});
