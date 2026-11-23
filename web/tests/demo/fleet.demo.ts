import { expect, test } from '../e2e/fixtures';
import {
	PASSWORD,
	admin,
	caddyCA,
	call,
	exec,
	execAsync,
	invitationLink,
	login,
	ownerSQL,
	repoScript,
	setupToken,
	tamperDownload
} from './stack';

/**
 * The M1 demo (IMPLEMENTIERUNGSPLAN §4): a fresh installation, seven Linux hosts – Debian, Ubuntu,
 * the RHEL family and Fedora with systemd, Alpine with OpenRC – get
 * the agent with the one-liner from the UI, and within 2 minutes they show up with metrics. A user
 * of another organization sees none of it.
 */

const other = { name: 'Bea Beta', email: 'bea@example.org' };
/** running, stopped or what the init system says, for systemd and OpenRC. */
function serviceState(agent: { service: string; init: 'systemd' | 'openrc' }): string {
	if (agent.init === 'systemd') {
		const state = exec(agent.service, 'systemctl is-active sermon-agent || true').trim();
		return state === 'active' ? 'running' : state;
	}
	const state = exec(agent.service, 'rc-service sermon-agent status 2>&1 || true');
	return /started/.test(state) ? 'running' : /stopped/.test(state) ? 'stopped' : state.trim();
}

const agents = [
	{ service: 'agent-debian-12', host: 'web-01', trust: 'system', os: 'debian', init: 'systemd' },
	{
		service: 'agent-debian-13',
		host: 'web-02',
		trust: 'fingerprint',
		os: 'debian',
		init: 'systemd'
	},
	{ service: 'agent-ubuntu-24-04', host: 'db-01', trust: 'system', os: 'ubuntu', init: 'systemd' },
	// The RHEL family and Fedora (M2-19).
	{ service: 'agent-rockylinux-9', host: 'app-01', trust: 'system', os: 'rocky', init: 'systemd' },
	{
		service: 'agent-almalinux-10',
		host: 'app-02',
		trust: 'fingerprint',
		os: 'almalinux',
		init: 'systemd'
	},
	{ service: 'agent-fedora-44', host: 'dev-01', trust: 'system', os: 'fedora', init: 'systemd' },
	// Alpine with OpenRC and without journald (M2-20).
	{ service: 'agent-alpine-3-22', host: 'edge-01', trust: 'system', os: 'alpine', init: 'openrc' }
] as const;
/** Containers reach the proxy as sermon.test and the agent port of the server directly. */
const inside = 'https://sermon.test';
/** Fresh hosts for the signed agent packages (M2-21). */
const packageHosts = [
	{
		service: 'pkg-debian-13',
		host: 'pkg-01',
		init: 'systemd',
		install: 'apt-get install -y -qq /packages/*.deb',
		remove: 'apt-get remove -y -qq sermon-agent'
	},
	{
		service: 'pkg-rockylinux-9',
		host: 'pkg-02',
		init: 'systemd',
		install:
			'rpm --import /packages/gpg.pub.asc && rpm -K /packages/*.rpm | grep -q "signatures OK" && dnf install -y -q /packages/*.rpm',
		remove: 'dnf remove -y -q sermon-agent'
	},
	// apk refuses a package whose key is not in /etc/apk/keys.
	{
		service: 'pkg-alpine-3-22',
		host: 'pkg-03',
		init: 'openrc',
		install: 'cp /packages/sermon-agent.rsa.pub /etc/apk/keys/ && apk add -q /packages/*.apk',
		remove: 'apk del -q sermon-agent'
	}
] as const;
/** Trusts Caddy's CA system-wide: update-ca-certificates (Debian, Ubuntu, Alpine) or update-ca-trust (RHEL, Fedora). */
const trustCA =
	'if command -v update-ca-certificates >/dev/null; then cat > /usr/local/share/ca-certificates/sermon-e2e.crt && update-ca-certificates >/dev/null 2>&1; else cat > /etc/pki/ca-trust/source/anchors/sermon-e2e.crt && update-ca-trust; fi';
const agentURL = 'https://server:8443';

test('a fresh installation monitors seven hosts within two minutes, packages install and remove cleanly, and Org B sees nothing', async ({
	page,
	browser,
	baseURL
}) => {
	// Setup with the token from the server log, then the first login.
	await page.goto('/');
	await expect(page).toHaveURL(/\/setup/);
	await page.getByLabel('Setup token').fill(setupToken());
	await page.getByLabel('Organization name').fill('Alpha GmbH');
	await page.getByLabel('Name', { exact: true }).fill(admin.name);
	await page.getByLabel('Email address').fill(admin.email);
	await page.getByLabel('Password', { exact: true }).fill(PASSWORD);
	await page.getByLabel('Repeat password').fill(PASSWORD);
	await page.getByRole('button', { name: 'Set up' }).click();
	await expect(page).toHaveURL(/\/login\?reason=setup_done/);
	await login(page, admin.email);
	await expect(page.getByRole('heading', { name: 'No nodes yet' })).toBeVisible();

	// Nodes belong to a group: the add-node wizard leads to the group management.
	await page.getByRole('link', { name: 'Add node' }).click();
	await page.getByRole('link', { name: 'Create a group' }).click();
	await page.getByRole('tab', { name: 'Groups' }).click();
	await page.getByLabel('New group').fill('servers');
	await page.getByRole('button', { name: 'Create', exact: true }).click();
	await expect(
		page.getByRole('tabpanel', { name: 'Groups' }).getByText('servers', { exact: true })
	).toBeVisible();

	// One token for all hosts.
	await page.goto('/nodes/add');
	await page.getByLabel('Uses').fill(String(agents.length + packageHosts.length));
	await page.getByRole('button', { name: 'Create command' }).click();
	const command = (await page.locator('pre').textContent()) ?? '';
	expect(command).toContain(' export SERMON_ENROLLMENT_TOKEN=sme_');
	expect(command).toContain('sudo --preserve-env=SERMON_ENROLLMENT_TOKEN sh');

	// Two hosts trust Caddy's CA system-wide; one pins the intermediate CA and gets the variant
	// for servers without a public certificate, which checks install.sh by its checksum.
	const ca = caddyCA();
	await page.getByLabel('The server has no publicly trusted certificate', { exact: false }).check();
	await page
		.getByLabel('Fingerprint of the server certificate or a CA')
		.fill(ca.intermediateFingerprint);
	await expect(page.locator('pre')).toContainText('sha256sum -c -');
	const pinned = (await page.locator('pre').textContent()) ?? '';

	// Run the command from the UI on each host – only the address differs inside the network.
	const forCommand = (agent: (typeof agents)[number]) =>
		(agent.trust === 'system' ? command : pinned).replaceAll(
			new URL(baseURL ?? '').origin,
			inside
		) + ` --agent-url ${agentURL}`;

	// Counter-check of the supply chain: a modified binary is refused before anything is installed.
	const restore = tamperDownload();
	try {
		const refused = exec(agents[1].service, `${forCommand(agents[1])} 2>&1 || echo "exit=$?"`);
		expect(refused).toContain('signature check failed');
		expect(
			exec(agents[1].service, 'test -e /usr/local/bin/sermon-agent || echo absent').trim()
		).toBe('absent');
	} finally {
		restore();
	}

	const started = Date.now();
	for (const agent of agents) {
		if (agent.trust === 'system') {
			exec(agent.service, trustCA, ca.root);
		}
		// Without journald the agent reads busybox syslogd's files; they belong to wheel otherwise.
		const extra = agent.init === 'openrc' ? ' --grant-log-access' : '';
		const output = exec(agent.service, `${forCommand(agent)}${extra} 2>&1`);
		expect(output, `${agent.service}:\n${output}`).toContain('signature ok');
		expect(serviceState(agent)).toBe('running');
	}

	// OpenRC stops and starts the agent, which runs as sermon (M2-20).
	const alpine = agents.find((a) => a.init === 'openrc')!;
	exec(alpine.service, 'rc-service sermon-agent stop >/dev/null 2>&1');
	expect(serviceState(alpine)).toBe('stopped');
	exec(alpine.service, 'rc-service sermon-agent start >/dev/null 2>&1');
	expect(serviceState(alpine)).toBe('running');
	expect(exec(alpine.service, "ps -o user,args | grep '[s]ermon-agent run'")).toMatch(/^sermon\s/);

	// The wizard sees the first host; the fleet shows all of them with metrics in under 2 minutes.
	await expect(page.getByText(/Connected: /)).toBeVisible({ timeout: 60_000 });
	await page.goto('/fleet');
	const deadline = 120_000 - (Date.now() - started);
	await expect(page.getByRole('article')).toHaveCount(agents.length, { timeout: deadline });
	for (const agent of agents) {
		const tile = page.getByRole('article').filter({ hasText: agent.host });
		await expect(tile.getByRole('meter', { name: 'CPU' })).toBeVisible({
			timeout: 120_000 - (Date.now() - started)
		});
		// The host facts name the distribution (os-release ID).
		await expect(tile).toContainText(agent.os);
	}
	const elapsed = Math.round((Date.now() - started) / 1000);
	console.log(`${agents.length} hosts visible with metrics after ${elapsed} s`);

	// Resource budget (M2-25, KONZEPT §4.1): < 1 % of a core and < 30 MiB RSS over 60 s under
	// systemd with the journal source, journalctl included. Two hosts nothing else touches measure
	// in the background while the demo goes on.
	const budget = repoScript('agent-budget.sh');
	const budgetHosts = agents.filter(
		(a) => a.service === 'agent-debian-12' || a.service === 'agent-rockylinux-9'
	);
	// Since M3-04 with log shipping: 20 journal lines per second during the measurement, each with
	// a secret the agent redacts before its buffer.
	const load = budgetHosts.map((a) =>
		execAsync(
			a.service,
			'for i in $(seq 1200); do logger -t budget-load "load line $i password=hunter2"; sleep 0.05; done'
		)
	);
	const measuring = budgetHosts.map((a) => execAsync(a.service, 'sh -s -- 60 1 30', budget));
	await page.screenshot({ path: 'test-results/demo-fleet.png', fullPage: true });

	// The node detail shows host facts and charts.
	await page.getByRole('link', { name: 'web-01' }).click();
	await expect(page.getByRole('heading', { level: 1, name: 'web-01' })).toBeVisible();
	const nodeURL = new URL(page.url()).pathname;
	await expect(page.getByText(/debian\s+12\s+·\s+Kernel/)).toBeVisible();
	const cpu = page.getByRole('figure', { name: 'CPU by mode' });
	await expect(cpu.locator('canvas').first()).toBeVisible();
	// The 1-hour range comes from the database: the first 10-s windows arrive after ~20 s.
	await expect(cpu.getByText('No data in the selected time range.')).toHaveCount(0, {
		timeout: Math.max(5_000, 120_000 - (Date.now() - started))
	});
	console.log(`node detail with chart data after ${Math.round((Date.now() - started) / 1000)} s`);
	// With journald the source needs no notice (M2-31).
	await expect(page.getByText(/No security events|not verified/)).toHaveCount(0);
	await page.screenshot({ path: 'test-results/demo-node.png', fullPage: true });

	// Without journald anyone can write such lines (ADR 0025): twelve failed logins, forged by an
	// unprivileged user on the Alpine host, fire the brute-force alert – marked as unverified.
	exec(
		alpine.service,
		`for i in $(seq 12); do su nobody -s /bin/sh -c "logger -p auth.info -t sshd 'Failed password for root from 192.0.2.99 port 22 ssh2'"; done`
	);
	await page.goto('/alerts');
	const forged = page
		.getByRole('article')
		.filter({ hasText: 'edge-01' })
		.filter({ hasText: 'SSH brute force' });
	await expect(forged).toBeVisible({ timeout: 120_000 });
	await expect(forged.getByRole('note')).toContainText('Source not verified');
	await page.screenshot({ path: 'test-results/demo-unverified.png', fullPage: true });
	// Its node page names the source (M2-31): /var/log/messages, readable with --grant-log-access.
	await page.goto('/fleet');
	await page.getByRole('link', { name: 'edge-01' }).click();
	await expect(page.getByRole('status').filter({ hasText: 'not verified' })).toBeVisible();

	// State collectors under the hardened unit (M2-22): every host reports whether its clock is
	// synchronised (adjtimex, allowed by the syscall filter), and a failed systemd unit fires an
	// alert with its name, which resolves once the unit is reset.
	const fleet = await call<{ nodes?: { nodeId: string; hostname: string }[] }>(
		page.request,
		baseURL ?? '',
		'FleetService/GetFleetSummary',
		{}
	);
	const nodeIds = (fleet.nodes ?? []).map((n) => n.nodeId);
	await expect
		.poll(
			async () => {
				const res = await call<{ series?: { nodeId: string }[] }>(
					page.request,
					baseURL ?? '',
					'MetricsService/QueryMetrics',
					{
						selector: { metric: 'sermon_time_synchronized', nodeIds },
						from: new Date(Date.now() - 10 * 60_000).toISOString(),
						to: new Date().toISOString()
					}
				);
				return new Set((res.series ?? []).map((s) => s.nodeId)).size;
			},
			{ timeout: 60_000 }
		)
		.toBe(agents.length);
	const unitHost = agents.find((a) => a.service === 'agent-debian-13')!;
	exec(unitHost.service, 'systemd-run --quiet --unit=e2e-backup false');
	await page.goto('/alerts');
	const unitAlert = page
		.getByRole('article')
		.filter({ hasText: unitHost.host })
		.filter({ hasText: 'e2e-backup.service' });
	await expect(unitAlert).toContainText('systemd unit failed', { timeout: 180_000 });
	// Firing after `for: 1m`; reset while pending, it would simply end without a resolution.
	await expect(unitAlert).toContainText('Warning', { timeout: 180_000 });
	exec(unitHost.service, 'systemctl reset-failed e2e-backup.service');
	// The agent reports 0 for the recovered unit: the alert resolves and leaves the list. Without
	// that 0 (missing data) it would keep firing.
	await expect(unitAlert).toHaveCount(0, { timeout: 120_000 });
	console.log(
		`failed unit alerted and resolved after ${Math.round((Date.now() - started) / 1000)} s`
	);

	// The budget holds; journalctl counts (agent and child). Counter-check: with the limits lowered
	// to nothing, the same measurement fails and names both.
	for (const [i, result] of (await Promise.all(measuring)).entries()) {
		console.log(`${budgetHosts[i].host}: ${result.output.trim()}`);
		expect(result.status, `${budgetHosts[i].host}:\n${result.output}`).toBe(0);
		expect(result.output).toMatch(/, [2-9] processes/);
	}
	await Promise.all(load);
	// The agents shipped the load lines, redacted (their own metrics, M3-04).
	for (const host of budgetHosts) {
		const counter = (metric: string) =>
			Number(
				ownerSQL(`SELECT coalesce(max(r.max), 0) FROM metrics_raw r JOIN series s ON s.id = r.series_id
					JOIN nodes n ON n.id = s.node_id WHERE n.hostname = '${host.host}' AND s.metric = '${metric}'`).trim()
			);
		await expect
			.poll(() => counter('sermon_agent_logs_redacted_entries'), { timeout: 60_000 })
			.toBeGreaterThanOrEqual(1000);
		expect(counter('sermon_agent_logs_sent_entries')).toBeGreaterThanOrEqual(1000);
		expect(counter('sermon_agent_logs_dropped_entries')).toBe(0);
		// …and they are in Loki (M3-07), in the tenant of the node's organization, redacted. Loki
		// has no port: asked from the database container on the same internal network.
		const [nodeID, orgID] = ownerSQL(`SELECT id, org_id FROM nodes WHERE hostname = '${host.host}'`)
			.trim()
			.split('|');
		const query = encodeURIComponent(`{node_id="${nodeID}"} |= "load line"`);
		const lokiLines = (tenant: string) =>
			exec(
				'postgres',
				`wget -qO- --header 'X-Scope-OrgID: ${tenant}' 'http://loki:3100/loki/api/v1/query_range?query=${query}&limit=5000&since=10m'`
			);
		await expect
			.poll(() => (lokiLines(orgID).match(/load line \d+ password=\*\*\*/g) ?? []).length, {
				timeout: 60_000
			})
			.toBeGreaterThanOrEqual(1000);
		const stored = lokiLines(orgID);
		expect(stored).not.toContain('hunter2');
		expect(stored).toContain('budget-load');
		// Another organization's tenant sees none of it.
		expect(lokiLines('00000000-0000-7000-8000-000000000000')).not.toContain('load line');
	}
	const lowered = await execAsync(budgetHosts[0].service, 'sh -s -- 3 0 1', budget);
	expect(lowered.status, lowered.output).toBe(1);
	expect(lowered.output).toContain('CPU over budget');
	expect(lowered.output).toContain('RSS over budget');

	// Signed packages (M2-21): install, set the server, enroll as sermon, start; the node reports.
	// Removal stops the agent and leaves nothing but the configuration.
	const token = /SERMON_ENROLLMENT_TOKEN=(sme_\S+)/.exec(command)?.[1] ?? '';
	expect(token).not.toBe('');
	for (const host of packageHosts) {
		exec(host.service, trustCA, ca.root);
		exec(host.service, `${host.install} 2>&1`);
		exec(
			host.service,
			`sed -i 's#^url = .*#url = "${inside}"#' /etc/sermon/agent.toml && echo 'agent_url = "${agentURL}"' >> /etc/sermon/agent.toml`
		);
		exec(
			host.service,
			'runuser -u sermon -- /usr/bin/sermon-agent enroll --config /etc/sermon/agent.toml 2>&1',
			undefined,
			{
				SERMON_ENROLLMENT_TOKEN: token
			}
		);
		exec(
			host.service,
			host.init === 'systemd'
				? 'systemctl enable --now sermon-agent 2>&1'
				: 'rc-update add sermon-agent default >/dev/null && rc-service sermon-agent start 2>&1'
		);
		expect(serviceState(host)).toBe('running');
	}
	await page.goto('/fleet');
	for (const host of packageHosts) {
		const tile = page.getByRole('article').filter({ hasText: host.host });
		await expect(tile.getByRole('meter', { name: 'CPU' })).toBeVisible({ timeout: 60_000 });
	}
	for (const host of packageHosts) {
		exec(host.service, `${host.remove} 2>&1`);
		expect(
			exec(host.service, "grep -ls '[s]ermon-agent' /proc/[0-9]*/cmdline | wc -l").trim()
		).toBe('0');
		expect(
			exec(
				host.service,
				'for p in /usr/bin/sermon-agent /var/lib/sermon-agent; do test ! -e $p || echo $p; done'
			).trim()
		).toBe('');
	}
	console.log(
		`packages installed, reporting and removed after ${Math.round((Date.now() - started) / 1000)} s`
	);

	// Tenant isolation: the superadmin creates Org B; its admin sees none of Alpha's nodes. In
	// production the first admin's link goes out by mail only (M1-39).
	const created = await call<{ invitationToken?: string; invitationEmailed?: boolean }>(
		page.request,
		baseURL ?? '',
		'OrganizationService/CreateOrganization',
		{
			name: 'Beta AG',
			adminEmail: other.email
		}
	);
	expect(created.invitationToken ?? '').toBe('');
	expect(created.invitationEmailed).toBe(true);
	const link = await invitationLink(other.email);
	expect(new URL(link).origin).toBe(new URL(baseURL ?? '').origin);
	const context = await browser.newContext({ ignoreHTTPSErrors: true });
	const bea = await context.newPage();
	await bea.goto(link);
	await bea.getByLabel('Name', { exact: true }).fill(other.name);
	await bea.getByLabel('Password', { exact: true }).fill(PASSWORD);
	await bea.getByLabel('Repeat password').fill(PASSWORD);
	await bea.getByRole('button', { name: 'Create account and join' }).click();
	await expect(bea).toHaveURL(/\/login\?reason=invited/);
	await login(bea, other.email);
	await expect(bea.getByRole('heading', { name: 'No nodes yet' })).toBeVisible();
	await bea.goto(nodeURL);
	await expect(bea.getByRole('heading', { name: 'Node not found' })).toBeVisible();
	const summary = await call<{ nodes?: unknown[] }>(
		bea.request,
		baseURL ?? '',
		'FleetService/GetFleetSummary',
		{}
	);
	expect(summary.nodes ?? []).toHaveLength(0);
	await context.close();
});
