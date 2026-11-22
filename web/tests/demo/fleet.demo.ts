import type { APIRequestContext, Page } from '@playwright/test';
import { expect, test } from '../e2e/fixtures';
import { caddyCA, exec, invitationLink, setupToken, tamperDownload } from './stack';

/**
 * The M1 demo (IMPLEMENTIERUNGSPLAN §4): a fresh installation, seven Linux hosts – Debian, Ubuntu,
 * the RHEL family and Fedora with systemd, Alpine with OpenRC – get
 * the agent with the one-liner from the UI, and within 2 minutes they show up with metrics. A user
 * of another organization sees none of it.
 */

// Test fixtures for a throwaway installation, not secrets.
const PASSWORD = 'correct horse battery staple'; // betterleaks:allow
const admin = { name: 'Ann Admin', email: 'ann@example.org' };
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

async function login(page: Page, email: string) {
	await page.goto('/login');
	await page.getByLabel('Email address').fill(email);
	await page.getByLabel('Password', { exact: true }).fill(PASSWORD);
	await page.getByRole('button', { name: 'Sign in' }).click();
	await expect(page).toHaveURL('/fleet');
}

/** A Connect call with the cookies of the page (the server checks the origin). */
async function call<T>(
	request: APIRequestContext,
	baseURL: string,
	method: string,
	body: object
): Promise<T> {
	const res = await request.post(`/sermon.web.v1.${method}`, {
		data: body,
		headers: { Origin: new URL(baseURL).origin, 'Content-Type': 'application/json' }
	});
	expect(res.ok(), `${method}: ${res.status()} ${await res.text()}`).toBe(true);
	return (await res.json()) as T;
}

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
