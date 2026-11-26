import { expect, test } from '../e2e/fixtures';
import { execAsync, repoScript } from './stack';

/**
 * The resource budget at the end of the demo (M4-32, KONZEPT §4.1): after every scene – live
 * views, log load, update runs, a reboot, alerts – the agents still stay below 1 % of a core and
 * 30 MiB, private memory and RSS alike. musl's allocator kept memory the agent had freed, up to
 * 26 MiB after a long run; with jemalloc it stays at a fixed few MiB (ADR 0038).
 */

const hosts = [
	{ service: 'agent-debian-12', host: 'web-01' },
	{ service: 'agent-rockylinux-9', host: 'app-01' },
	{ service: 'agent-ubuntu-24-04', host: 'db-01' }
];

test('the agents stay within their budget at the end of the run', async () => {
	test.setTimeout(3 * 60_000);
	const budget = repoScript('agent-budget.sh');
	const results = await Promise.all(
		hosts.map((h) => execAsync(h.service, 'sh -s -- 60 1 30', budget))
	);
	for (const [i, result] of results.entries()) {
		console.log(`${hosts[i].host} at the end: ${result.output.trim()}`);
		expect(result.status, `${hosts[i].host}:\n${result.output}`).toBe(0);
		const rss = Number(/rss peak ([\d.]+) MiB/.exec(result.output)?.[1]);
		expect(rss, `${hosts[i].host}: RSS`).toBeLessThan(30);
	}
});
