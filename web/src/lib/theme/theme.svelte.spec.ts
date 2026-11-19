import { afterEach, describe, expect, it } from 'vitest';
import { isDark, THEME_KEY, themeState } from './theme.svelte.js';

describe('theme', () => {
	afterEach(() => themeState.set('system'));

	it('resolves the system theme from the operating system', () => {
		expect(isDark('system', true)).toBe(true);
		expect(isDark('system', false)).toBe(false);
		expect(isDark('dark', false)).toBe(true);
		expect(isDark('light', true)).toBe(false);
	});

	it('applies and remembers the choice', () => {
		themeState.set('dark');
		expect(document.documentElement.classList.contains('dark')).toBe(true);
		expect(localStorage.getItem(THEME_KEY)).toBe('dark');

		themeState.set('light');
		expect(document.documentElement.classList.contains('dark')).toBe(false);
		expect(localStorage.getItem(THEME_KEY)).toBe('light');
	});
});
