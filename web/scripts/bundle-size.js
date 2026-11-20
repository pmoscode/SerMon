// Bundle size guard: fails if all gzipped JavaScript of the build together exceeds the limit (catches
// accidental large dependencies). The first-load budget of KONZEPT §17 (< 200 KiB per page) is measured
// in the browser: tests/e2e/budget.e2e.ts.
// Usage: node scripts/bundle-size.js [buildDir] [limitKiB]
import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { argv, exit } from 'node:process';
import { fileURLToPath } from 'node:url';
import { gzipSync } from 'node:zlib';

/** Returns the summed gzipped size in bytes of all files with the given extension below dir. */
export function gzippedSize(dir, extension) {
	let total = 0;
	for (const entry of readdirSync(dir, { withFileTypes: true })) {
		const path = join(dir, entry.name);
		if (entry.isDirectory()) total += gzippedSize(path, extension);
		else if (entry.name.endsWith(extension)) total += gzipSync(readFileSync(path)).length;
	}
	return total;
}

if (argv[1] === fileURLToPath(import.meta.url)) {
	const [dir = 'build', limitKiB = '400'] = argv.slice(2);
	const size = gzippedSize(dir, '.js');
	const limit = Number(limitKiB) * 1024;
	const kib = (size / 1024).toFixed(1);
	if (size > limit) {
		console.error(`✗ JavaScript bundle ${kib} KiB gzip exceeds budget of ${limitKiB} KiB`);
		exit(1);
	}
	console.log(`✓ JavaScript bundle ${kib} KiB gzip (budget ${limitKiB} KiB)`);
}
