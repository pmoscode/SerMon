import AxeBuilder from '@axe-core/playwright';
import { expect, test } from '../e2e/fixtures';
import {
	PASSWORD,
	admin,
	ageStepUp,
	call,
	exec,
	execAsync,
	login,
	ownerSQL,
	repoScript
} from './stack';

/**
 * The M5 demo (IMPLEMENTIERUNGSPLAN M5-32), on the installation of fleet.demo.ts: the agent of
 * web-01 found nginx, the fleet suggests the module, an admin enables it for the group with the
 * step-up and nginx's metrics arrive. Then every module of waves ① and ② runs on web-01 – the
 * host has Apache, Redis, PostgreSQL and MariaDB too (deploy/e2e) – and the agent stays within its
 * budget (M5-31). nginx stops: "nginx down" fires (its default rule, 2 minutes) and ends once nginx
 * is back. The modules keep running to the end of the demo, where budget.demo.ts measures again.
 */

// The step-up asks once with FAILED_PRECONDITION (400), the browser notes it in the console.
test.use({ expectedHttpErrors: [400, 401] });

const web01 = { service: 'agent-debian-12', host: 'web-01' };
const run = Date.now().toString(36);
// The monitoring users' password of this run, delivered to the agent as a module credential.
const monitorPassword = `monitor-${run}`; // betterleaks:allow

type NodeModules = {
	groupId?: string;
	settings?: { moduleId: string; enabled?: boolean }[];
	health?: { moduleId: string; state?: string; reason?: string }[];
};
type Alerts = { alerts?: { key?: { ruleId?: string; nodeId?: string }; state?: string }[] };

/**
 * The other modules of waves ① and ② on web-01 (M5-31), each against a service that runs there;
 * PostgreSQL and MariaDB with a monitoring user of few rights.
 */
const others: { moduleId: string; config: object; secrets?: Record<string, string> }[] = [
	{ moduleId: 'http_check', config: { urls: ['http://127.0.0.1:8080/server-status?auto'] } },
	{
		moduleId: 'tcp_check',
		config: { targets: ['127.0.0.1:8080', '127.0.0.1:6379', '127.0.0.1:5432', '127.0.0.1:3306'] }
	},
	{
		moduleId: 'tls',
		config: { files: ['/etc/ssl/e2e/web-01.pem'], ca_file: '/etc/ssl/e2e/ca.pem' }
	},
	{
		moduleId: 'systemd_units',
		config: {
			units: [
				'ssh.service',
				'apache2.service',
				'redis-server.service',
				'postgresql.service',
				'mariadb.service'
			]
		}
	},
	{ moduleId: 'apache', config: { url: 'http://127.0.0.1:8080/server-status?auto' } },
	{ moduleId: 'redis', config: {} },
	{ moduleId: 'postgres', config: { role: 'sermon' }, secrets: { secret: monitorPassword } },
	{ moduleId: 'mysql', config: { user: 'sermon' }, secrets: { secret: monitorPassword } }
];

/** A CA and a certificate of web-01 it signed, valid for a year, below /etc/ssl (module tls). */
const certificates = `set -e
mkdir -p /etc/ssl/e2e && cd /tmp
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 365 -subj /CN=e2e-ca -keyout ca.key -out /etc/ssl/e2e/ca.pem 2>/dev/null
openssl req -new -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -subj /CN=web-01 -keyout web.key -out web.csr 2>/dev/null
printf 'subjectAltName=DNS:web-01\\nextendedKeyUsage=serverAuth\\n' > web.ext
openssl x509 -req -in web.csr -CA /etc/ssl/e2e/ca.pem -CAkey ca.key -CAcreateserial -days 365 -extfile web.ext -out /etc/ssl/e2e/web-01.pem 2>/dev/null
rm -f ca.key web.key web.csr web.ext /etc/ssl/e2e/ca.srl
chmod 0644 /etc/ssl/e2e/*.pem`;

test('nginx is suggested, enabled with the step-up, delivers metrics and alerts when it stops; all modules stay within the budget', async ({
	page,
	baseURL
}) => {
	test.setTimeout(15 * 60_000);
	const base = baseURL ?? '';
	await login(page, admin.email);
	const fleet = await call<{ nodes?: { nodeId: string; hostname: string }[] }>(
		page.request,
		base,
		'FleetService/GetFleetSummary',
		{}
	);
	const nodeId = fleet.nodes!.find((n) => n.hostname === web01.host)!.nodeId;

	// web-01 gets a group of its own: modules are set per group, and the other hosts run no nginx.
	// There is no UI to move nodes between groups yet: the demo moves it in the database
	// (arrangement only, as patch.demo.ts).
	const group = await call<{ group?: { id: string } }>(
		page.request,
		base,
		'AccessService/CreateNodeGroup',
		{ name: 'web' }
	);
	const groupId = group.group!.id;
	ownerSQL(`UPDATE nodes SET group_id = '${groupId}' WHERE id = '${nodeId}'`);

	// The suggestion in the fleet: the agent found nginx (process, unit, port 80). Enabling leads
	// to the group's form with the agent's proposal; saving asks for the password again.
	ageStepUp(admin.email);
	await page.goto('/fleet');
	const hint = page
		.getByTestId('fleet-module-hint')
		.filter({ hasText: 'nginx web server detected on web-01 – enable the module?' });
	await expect(hint).toBeVisible({ timeout: 30_000 });
	await page.screenshot({ path: 'test-results/demo-module-hint.png', fullPage: true });
	await hint.getByRole('link', { name: 'Enable' }).click();
	await expect(page.getByRole('tab', { name: 'Modules' })).toHaveAttribute('aria-selected', 'true');
	await expect(page.getByLabel('Status URL')).toHaveValue('http://127.0.0.1/nginx_status');
	expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
	await page.getByRole('button', { name: 'Save' }).click();
	const stepUp = page.getByRole('dialog');
	await stepUp.getByLabel('Password').fill(PASSWORD);
	await stepUp.getByRole('button', { name: 'Confirm' }).click();
	const enabled = Date.now();
	await expect(
		page
			.getByRole('region', { name: 'web', exact: true })
			.getByRole('listitem')
			.filter({ hasText: 'nginx web server' })
	).toContainText('On');

	// nginx's metrics arrive: up, connections and – from the second run – the request rate (the
	// module's own requests count).
	const latest = async (metric: string) => {
		const res = await call<{ series?: { points?: { avg?: number }[] }[] }>(
			page.request,
			base,
			'MetricsService/QueryMetrics',
			{
				selector: { metric, nodeIds: [nodeId] },
				from: new Date(Date.now() - 5 * 60_000).toISOString(),
				to: new Date().toISOString()
			}
		);
		return res.series?.[0]?.points?.at(-1)?.avg;
	};
	await expect
		.poll(
			async () => ({
				up: await latest('sermon_nginx_up'),
				active: ((await latest('sermon_nginx_connections_active')) ?? 0) >= 1,
				requests: ((await latest('sermon_nginx_requests_per_second')) ?? 0) > 0
			}),
			{ timeout: 120_000, intervals: [5_000] }
		)
		.toEqual({ up: 1, active: true, requests: true });
	console.log(`nginx metrics after ${Math.round((Date.now() - enabled) / 1000)} s`);
	// The node's tab: the module runs, with its metrics.
	await page.goto(`/nodes/${nodeId}?tab=modules`);
	const nginx = page.getByRole('region', { name: 'nginx web server' });
	await expect(nginx.getByTestId('module-state')).toHaveText('Running', { timeout: 30_000 });
	expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
	await page.screenshot({ path: 'test-results/demo-module-node.png', fullPage: true });

	// The rest of waves ① and ② (M5-31), set up like the form does (the step-up is fresh). The
	// databases get a monitoring user each, its password goes to the agent as a credential.
	exec(web01.service, certificates);
	exec(
		web01.service,
		`runuser -u postgres -- psql -qc "CREATE ROLE sermon LOGIN PASSWORD '$PW' IN ROLE pg_monitor" &&
		 mariadb -e "CREATE USER 'sermon'@'127.0.0.1' IDENTIFIED BY '$PW'; GRANT PROCESS, REPLICA MONITOR ON *.* TO 'sermon'@'127.0.0.1'"`,
		undefined,
		{ PW: monitorPassword }
	);
	for (const m of others) {
		await call(page.request, base, 'ModuleService/SetGroupModule', {
			groupId,
			setting: { moduleId: m.moduleId, enabled: true, configJson: JSON.stringify(m.config) },
			secrets: m.secrets ?? {}
		});
	}
	const all = ['nginx', ...others.map((m) => m.moduleId)].sort();

	// nginx stops. Meanwhile – the rule waits 2 minutes – every module reports, and the agent with
	// all of them is measured against its budget.
	exec(web01.service, 'systemctl stop nginx');
	const stopped = Date.now();
	const measuring = execAsync(web01.service, 'sh -s -- 60 1 30', repoScript('agent-budget.sh'));
	const health = async () =>
		(await call<NodeModules>(page.request, base, 'ModuleService/GetNodeModules', { nodeId }))
			.health ?? [];
	await expect
		.poll(
			async () =>
				(await health())
					.map(
						(h) =>
							`${h.moduleId}:${h.state === 'MODULE_STATE_OK' ? 'ok' : `${h.state} ${h.reason}`}`
					)
					.sort(),
			{ timeout: 120_000, intervals: [5_000] }
		)
		.toEqual(all.map((id) => `${id}:ok`));
	// Each delivers its values: one metric per module.
	const sample: Record<string, string> = {
		http_check: 'sermon_http_check_up',
		tcp_check: 'sermon_tcp_check_up',
		tls: 'sermon_tls_chain_valid',
		systemd_units: 'sermon_systemd_units_active',
		apache: 'sermon_apache_up',
		redis: 'sermon_redis_up',
		postgres: 'sermon_postgres_connections_limit',
		mysql: 'sermon_mysql_connections_limit'
	};
	await expect
		.poll(
			async () => {
				const missing = [];
				for (const [id, metric] of Object.entries(sample)) {
					if (!((await latest(metric)) ?? 0)) missing.push(id);
				}
				return missing;
			},
			{ timeout: 120_000, intervals: [10_000] }
		)
		.toEqual([]);
	const budget = await measuring;
	console.log(`web-01 with ${all.length} modules: ${budget.output.trim()}`);
	expect(budget.status, budget.output).toBe(0);
	const rss = Number(/rss peak ([\d.]+) MiB/.exec(budget.output)?.[1]);
	expect(rss, 'RSS').toBeLessThan(30);
	// Counter-check: with the limits lowered to nothing, the same measurement fails on both.
	const lowered = await execAsync(web01.service, 'sh -s -- 3 0 1', repoScript('agent-budget.sh'));
	expect(lowered.status, lowered.output).toBe(1);
	expect(lowered.output).toContain('CPU over budget');
	expect(lowered.output).toContain('memory over budget');

	// "nginx down" fires after its 2 minutes – and is the only alert of a module: the others watch
	// services that run.
	await page.goto('/alerts');
	const alert = page
		.getByRole('article')
		.filter({ hasText: web01.host })
		.filter({ hasText: 'nginx down' });
	await expect(alert).toContainText('Critical', { timeout: 240_000 });
	console.log(`nginx down alerted after ${Math.round((Date.now() - stopped) / 1000)} s`);
	const alerts = await call<Alerts>(page.request, base, 'AlertService/ListAlerts', {});
	expect(
		(alerts.alerts ?? [])
			.filter((a) => a.key?.nodeId === nodeId && a.key?.ruleId?.startsWith('module.'))
			.map((a) => `${a.key?.ruleId}:${a.state}`)
	).toEqual(['module.nginx.down:ALERT_STATE_CRITICAL']);
	expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
	await page.screenshot({ path: 'test-results/demo-module-alert.png', fullPage: true });

	// nginx is back: the alert ends and leaves the list.
	exec(web01.service, 'systemctl start nginx');
	const restarted = Date.now();
	await expect(alert).toHaveCount(0, { timeout: 120_000 });
	console.log(`nginx down resolved after ${Math.round((Date.now() - restarted) / 1000)} s`);
	await expect.poll(() => latest('sermon_nginx_up'), { timeout: 60_000 }).toBe(1);
});
