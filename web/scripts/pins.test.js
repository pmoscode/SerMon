import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

// Supply chain (KONZEPT, "Pipelines"): every dependency is pinned to one exact version, so that an
// update only comes through a reviewed lockfile change. Generators (shadcn-svelte) like to add "^".
describe('package.json', () => {
	const pkg = JSON.parse(readFileSync(new URL('../package.json', import.meta.url), 'utf8'));
	const deps = { ...pkg.dependencies, ...pkg.devDependencies };

	it.each(Object.entries(deps))('pins %s exactly', (_name, version) => {
		expect(version).toMatch(/^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/);
	});
});
