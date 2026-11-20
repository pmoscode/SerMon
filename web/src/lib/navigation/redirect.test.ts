import { describe, expect, it } from 'vitest';
import { HOME, loginPath, safeRedirect } from './redirect.js';

describe('safeRedirect', () => {
	it.each([
		['/nodes/42', '/nodes/42'],
		['/fleet?group=a#top', '/fleet?group=a#top'],
		['/fleet/../nodes', '/nodes']
	])('keeps the app path %s', (target, want) => {
		expect(safeRedirect(target)).toBe(want);
	});

	it.each([
		null,
		'',
		'https://evil.example/',
		'//evil.example/x',
		'/\\evil.example',
		// Paths that become protocol-relative only after normalization.
		'/.//evil.example/x',
		'/a/..//evil.example',
		'/.\\/evil.example',
		'/./\\evil.example',
		'javascript:alert(1)',
		'fleet',
		'/login?redirect=/x'
	])('falls back to the start page for %s', (target) => {
		expect(safeRedirect(target)).toBe(HOME);
	});
});

describe('loginPath', () => {
	it('carries the reason and a safe return path', () => {
		expect(loginPath('expired', '/nodes/42')).toBe('/login?reason=expired&redirect=%2Fnodes%2F42');
		expect(loginPath('logged_out')).toBe('/login?reason=logged_out');
		expect(loginPath(undefined, '//evil.example')).toBe('/login');
	});
});
