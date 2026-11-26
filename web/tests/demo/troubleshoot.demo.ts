import AxeBuilder from '@axe-core/playwright';
import { expect, test } from '../e2e/fixtures';
import { admin, call, exec, login, ownerSQL } from './stack';

/**
 * The M3 demo, "troubleshooting without SSH" (IMPLEMENTIERUNGSPLAN M3-26), on the installation of
 * fleet.demo.ts after the attack of alerts.demo.ts: a service on web-01 fails and logs a line with
 * a secret; the alert leads into the logs around its time, where the line stands redacted (also in
 * Loki). The security dashboard shows the brute force with its address on top, and a log file of
 * the Alpine host arrives over a glob of the group's log settings. The sign-in over Dex and the
 * step-up with a passkey are sso.demo.ts and passkeys.demo.ts; live tail and live view are in
 * fleet.demo.ts.
 */

const web01 = { service: 'agent-debian-12', host: 'web-01' };
const alpine = { service: 'agent-alpine-3-22', host: 'edge-01' };
const run = Date.now().toString(36);
const secret = `s3cr3t-${run}`; // betterleaks:allow

/** Loki's lines of a node with text, asked from the database container (Loki has no port). */
function lokiLines(host: string, text: string): string {
	const [nodeID, orgID] = ownerSQL(`SELECT id, org_id FROM nodes WHERE hostname = '${host}'`)
		.trim()
		.split('|');
	const query = encodeURIComponent(`{node_id="${nodeID}"} |= "${text}"`);
	return exec(
		'postgres',
		`wget -qO- --header 'X-Scope-OrgID: ${orgID}' 'http://loki:3100/loki/api/v1/query_range?query=${query}&limit=100&since=30m'`
	);
}

test('a failed service leads from its alert into the redacted logs; the dashboard and a log file glob work on real data', async ({
	page,
	baseURL
}) => {
	const base = baseURL ?? '';
	await login(page, admin.email);

	// The security dashboard with the brute force of alerts.demo.ts: its address on top, verified
	// (sshd's journal), with the events of web-01.
	const attacker = exec('agent-debian-13', 'hostname -i').trim().split(/\s+/)[0]!;
	await page.goto('/security?range=1h');
	const topIPs = page.getByRole('region', { name: 'Top IPs of failed logins' });
	await expect(topIPs.getByRole('button', { name: `Show events of ${attacker}` })).toBeVisible({
		timeout: 30_000
	});
	await expect(topIPs.getByRole('listitem').first()).toContainText(attacker);
	await topIPs.getByRole('button', { name: `Show events of ${attacker}` }).click();
	await expect(
		page.getByRole('table').getByRole('row').filter({ hasText: web01.host }).first()
	).toContainText(attacker);

	// A service fails and logs its secret on the way.
	const unit = `e2e-migrate-${run}`;
	exec(
		web01.service,
		`systemd-run --quiet --unit=${unit} sh -c 'echo "migration failed: password=${secret}"; exit 3'`
	);
	await page.goto('/alerts');
	const alert = page
		.getByRole('article')
		.filter({ hasText: web01.host })
		.filter({ hasText: `${unit}.service` });
	await expect(alert).toContainText('Warning', { timeout: 180_000 });
	expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);

	// From the alert into the logs of the node around its time: the line, redacted.
	await alert.getByRole('link', { name: 'Logs around this time' }).click();
	// Only the unit's lines (M4-28): the node's other lines can't push it out of the first 200.
	await expect(page).toHaveURL(/\/logs\?.*node=.*&unit=e2e-migrate-/);
	const lines = page.getByRole('region', { name: 'Lines' });
	await expect(lines.getByText('migration failed: password=***').first()).toBeVisible({
		timeout: 60_000
	});
	await expect(page.getByText(secret)).toHaveCount(0);
	await page.screenshot({ path: 'test-results/demo-m3-logs.png', fullPage: true });
	expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
	// Loki holds it redacted: the secret never left the agent.
	const stored = lokiLines(web01.host, 'migration failed');
	expect(stored).toContain('password=***');
	expect(stored).not.toContain(secret);
	exec(web01.service, `systemctl reset-failed ${unit}.service`);

	// A log file of the Alpine host over a glob of the group's settings (M3-05): the agent gets
	// the setting from the server (M3-03) and follows the file.
	const groups = await call<{ groups?: { id: string; name: string }[] }>(
		page.request,
		base,
		'AccessService/ListNodeGroups',
		{}
	);
	const group = ownerSQL(`SELECT group_id FROM nodes WHERE hostname = '${alpine.host}'`).trim();
	expect(groups.groups?.some((g) => g.id === group)).toBe(true);
	await call(page.request, base, 'LogSettingsService/SetGroupLogSettings', {
		groupId: group,
		settings: { enabled: true, files: [{ pattern: '/var/log/e2e/*.log' }] }
	});
	exec(alpine.service, 'mkdir -p /var/log/e2e && chmod 0755 /var/log/e2e');
	const stamp = `e2e glob ${run}`;
	await expect
		.poll(
			() => {
				exec(
					alpine.service,
					`echo "$(date -Iseconds) ${stamp} password=${secret}" >> /var/log/e2e/app.log && chmod 0644 /var/log/e2e/app.log`
				);
				return lokiLines(alpine.host, stamp);
			},
			{ timeout: 120_000, intervals: [5_000] }
		)
		.toContain(stamp);
	expect(lokiLines(alpine.host, stamp)).not.toContain(secret);
	await page.goto(`/logs?range=15m&q=${encodeURIComponent(stamp)}`);
	await expect(lines.getByText(stamp).first()).toBeVisible({ timeout: 30_000 });
	// Back to the journal only, as the other demos expect.
	await call(page.request, base, 'LogSettingsService/ResetGroupLogSettings', { groupId: group });
});
