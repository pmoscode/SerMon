import { beforeEach, describe, expect, it } from 'vitest';
import { getLocale, localStorageKey } from '#lib/paraglide/runtime.js';
import { localeState, switchLocale } from './locale.svelte';

describe('switchLocale', () => {
	beforeEach(() => switchLocale('en'));

	it('updates the reactive state and the paraglide runtime', () => {
		switchLocale('de');

		expect(localeState.current).toBe('de');
		expect(getLocale()).toBe('de');
	});

	it('sets the document language for screen readers and hyphenation', () => {
		switchLocale('de');

		expect(document.documentElement.lang).toBe('de');
	});

	it('remembers the choice across reloads', () => {
		switchLocale('de');

		expect(localStorage.getItem(localStorageKey)).toBe('de');
	});
});
