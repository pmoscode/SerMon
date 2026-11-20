import { describe, expect, it } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import type { Inline } from '#lib/help/types.js';
import HelpInline from './HelpInline.svelte';

const link = (href: string): Inline => ({
	type: 'link',
	href,
	content: [{ type: 'text', text: href }]
});

describe('HelpInline', () => {
	it('links app paths and https documentation', async () => {
		render(HelpInline, { content: [link('/fleet'), link('https://docs.example.org/')] });

		await expect
			.element(page.getByRole('link', { name: '/fleet' }))
			.toHaveAttribute('href', '/fleet');
		await expect
			.element(page.getByRole('link', { name: /docs\.example\.org/ }))
			.toHaveAttribute('target', '_blank');
	});

	// The build already rejects such targets; the component doesn't rely on it.
	it.each(['javascript:alert(1)', '//evil.example/x', '/\\evil.example', 'http://evil.example'])(
		'renders %s as text, not as a link',
		async (href) => {
			render(HelpInline, { content: [link(href)] });

			await expect.element(page.getByText(href)).toBeVisible();
			expect(document.querySelector('a')).toBeNull();
		}
	);
});
