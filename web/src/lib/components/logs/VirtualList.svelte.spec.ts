import { describe, expect, it } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { createRawSnippet } from 'svelte';
import VirtualList from './VirtualList.svelte';

const items = Array.from({ length: 5000 }, (_, i) => `item ${i}`);

const row = createRawSnippet((item: () => string) => ({
	render: () => `<div style="height: 30px">${item()}</div>`
}));

describe('VirtualList', () => {
	it('renders only the rows near the visible part', async () => {
		render(VirtualList<string>, {
			items,
			key: (s: string) => s,
			row,
			label: 'Items',
			estimate: 30,
			overscan: 300,
			height: '300px'
		});
		const region = page.getByRole('region', { name: 'Items' });
		await expect.element(region.getByText('item 0', { exact: true })).toBeVisible();
		const rendered = region.element().querySelectorAll('li').length;
		expect(rendered).toBeGreaterThan(5);
		expect(rendered).toBeLessThan(100);
		// The scroll height stands for all rows.
		expect(region.element().scrollHeight).toBeGreaterThan(5000 * 29);
		const first = region.element().querySelector('li')!;
		expect(first.getAttribute('aria-setsize')).toBe('5000');
		expect(first.getAttribute('aria-posinset')).toBe('1');

		region.element().scrollTop = 30 * 2500;
		region.element().dispatchEvent(new Event('scroll'));
		await expect.element(region.getByText('item 2500', { exact: true })).toBeInTheDocument();
		expect(region.element().querySelector('li')!.getAttribute('aria-posinset')).not.toBe('1');
		expect(region.element().querySelectorAll('li').length).toBeLessThan(100);
	});
});
