import { expect, test } from '../e2e/fixtures';
import { execAsync, ownerSQL, repoScript } from './stack';

/**
 * The resource budget at the end of the demo (M4-32, KONZEPT §4.1): after every scene – live
 * views, log load, update runs, a reboot, alerts – the agents still stay below 1 % of a core and
 * 30 MiB, private memory and RSS alike. musl's allocator kept memory the agent had freed, up to
 * 26 MiB after a long run; with jemalloc it stays at a fixed few MiB (ADR 0038). web-01 runs the
 * nine modules of waves ① and ② since modules.demo.ts (M5-31).
 */

const hosts = [
	{ service: 'agent-debian-12', host: 'web-01' },
	{ service: 'agent-rockylinux-9', host: 'app-01' },
	{ service: 'agent-ubuntu-24-04', host: 'db-01' }
];

test('the agents stay within their budget at the end of the run', async () => {
	test.setTimeout(3 * 60_000);
	// The modules of web-01 still run: each reported its last run as succeeded.
	const modules = ownerSQL(
		`SELECT string_agg(h.module_id || ':' || h.state, ',' ORDER BY h.module_id) FROM module_health h
		 JOIN nodes n ON n.id = h.node_id WHERE n.hostname = 'web-01'`
	).trim();
	expect(modules).toBe(
		'apache:1,http_check:1,mysql:1,nginx:1,postgres:1,redis:1,systemd_units:1,tcp_check:1,tls:1'
	);
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
