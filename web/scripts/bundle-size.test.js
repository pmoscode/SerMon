import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { gzipSync } from 'node:zlib';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { gzippedSize } from './bundle-size.js';

describe('gzippedSize', () => {
	let dir;

	beforeEach(() => {
		dir = mkdtempSync(join(tmpdir(), 'sermon-bundle-'));
	});
	afterEach(() => rmSync(dir, { recursive: true, force: true }));

	it('sums the gzipped size of all matching files, including nested ones', () => {
		const a = 'console.log("a");'.repeat(50);
		const b = 'export const b = 1;';
		writeFileSync(join(dir, 'a.js'), a);
		mkdirSync(join(dir, 'nested'));
		writeFileSync(join(dir, 'nested', 'b.js'), b);

		expect(gzippedSize(dir, '.js')).toBe(gzipSync(a).length + gzipSync(b).length);
	});

	it('ignores files with other extensions', () => {
		writeFileSync(join(dir, 'style.css'), 'body{}');
		writeFileSync(join(dir, 'font.woff2'), 'binary');

		expect(gzippedSize(dir, '.js')).toBe(0);
	});
});
