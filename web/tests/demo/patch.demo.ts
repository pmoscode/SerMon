import AxeBuilder from '@axe-core/playwright';
import { expect, test } from '../e2e/fixtures';
import { execFileSync } from 'node:child_process';
import { admin, call, compose, exec, login, ownerSQL } from './stack';

/**
 * The M4 demo, patch compliance at a glance (M4-28): a group of four nodes with the security
 * updates of this run's sources gets a security-auto policy whose window opens now. Its rollout
 * updates one node after the other; web-02 (Debian) reboots and passes the health check, its
 * outage mutes itself (no notification), db-01 vetoes remote updates and is skipped with that
 * reason. The dashboard of the install demo and the update by hand with a passkey (passkeys
 * demo) complete the story.
 */

const members = [
	{ service: 'agent-almalinux-10', host: 'app-02' },
	{ service: 'agent-ubuntu-24-04', host: 'db-01' },
	{ service: 'agent-fedora-44', host: 'dev-01' },
	{ service: 'agent-debian-13', host: 'web-02' }
] as const;
const rebooting = members[3];
const vetoing = members[1];

/** Sets a value of the host's helper.toml and restarts the helper, as its admin would. */
function helperSetting(service: string, key: string, value: boolean) {
	exec(
		service,
		`sed -i 's/^${key} = .*/${key} = ${value}/' /etc/sermon/helper.toml && grep -qx '${key} = ${value}' /etc/sermon/helper.toml && systemctl restart sermon-helper`
	);
}

type FleetUpdates = {
	nodes?: {
		nodeId: string;
		hostname: string;
		status?: string;
		permissions?: { remoteUpdates?: boolean; remoteReboot?: boolean; selfUpdate?: boolean };
	}[];
};

const weekdays = ['MONDAY', 'TUESDAY', 'WEDNESDAY', 'THURSDAY', 'FRIDAY', 'SATURDAY', 'SUNDAY'];

test('a security-auto policy updates a group one node at a time, reboots Debian without a notification and skips a veto', async ({
	page,
	baseURL
}) => {
	test.setTimeout(30 * 60_000);
	const base = baseURL ?? '';
	await login(page, admin.email);
	const fleet = await call<FleetUpdates>(page.request, base, 'UpdateService/ListFleetUpdates', {});
	const id = (host: string) => fleet.nodes!.find((n) => n.hostname === host)!.nodeId;
	for (const m of members) {
		expect(fleet.nodes!.find((n) => n.hostname === m.host)?.status, m.host).toBe(
			'UPDATE_STATUS_SECURITY'
		);
	}

	// The group of the rollout. There is no UI to move nodes between groups yet: the demo moves
	// them in the database (arrangement only).
	const group = await call<{ group?: { id: string } }>(
		page.request,
		base,
		'AccessService/CreateNodeGroup',
		{ name: 'patch' }
	);
	const groupId = group.group!.id;
	ownerSQL(
		`UPDATE nodes SET group_id = '${groupId}' WHERE id IN (${members.map((m) => `'${id(m.host)}'`).join(',')})`
	);

	// web-02 allows remote reboots, db-01 forbids remote updates; the next report says so.
	helperSetting(rebooting.service, 'allow_remote_reboot', true);
	helperSetting(vetoing.service, 'allow_remote_updates', false);
	for (const m of [rebooting, vetoing]) {
		await call(page.request, base, 'UpdateService/RefreshUpdateInfo', { nodeId: id(m.host) });
	}
	await expect
		.poll(
			async () => {
				const f = await call<FleetUpdates>(
					page.request,
					base,
					'UpdateService/ListFleetUpdates',
					{}
				);
				const p = (host: string) => f.nodes!.find((n) => n.hostname === host)?.permissions;
				return [p(rebooting.host)?.remoteReboot ?? false, p(vetoing.host)?.remoteUpdates ?? false];
			},
			{ timeout: 240_000, intervals: [5_000] }
		)
		.toEqual([true, false]);

	// Outages of the group reach the ntfy channel of the alerts demo – unless muted.
	const channels = await call<{ channels?: { id: string; type: string }[] }>(
		page.request,
		base,
		'NotificationChannelService/ListChannels',
		{}
	);
	await call(page.request, base, 'NotificationPolicyService/CreatePolicy', {
		spec: {
			name: 'Availability',
			enabled: true,
			matchers: { rules: ['system.node.offline', 'system.node.rebooted'] },
			channelIds: (channels.channels ?? []).filter((c) => c.type === 'ntfy').map((c) => c.id),
			grouping: 'POLICY_GROUPING_NODE',
			repeatMinutes: 5
		}
	});

	// web-02 boots slowly: its agent keeps running, but doesn't start with the next boot. Without
	// logind its reboot comes seconds after the command, not a minute (M4-28): set it beforehand.
	exec(rebooting.service, 'systemctl disable --quiet sermon-agent');

	// The policy: security updates automatically, a window from two minutes ago for two hours,
	// one node at a time, rebooting when needed, five minutes of health check.
	const now = new Date();
	const startMinute = (now.getUTCHours() * 60 + now.getUTCMinutes() - 2 + 1440) % 1440;
	const rolloutStarted = new Date();
	await call(page.request, base, 'UpdatePolicyService/SetUpdatePolicy', {
		groupId,
		spec: {
			mode: 'UPDATE_MODE_SECURITY_AUTO',
			window: {
				days: weekdays.map((d) => `WEEKDAY_${d}`),
				startMinute,
				durationMinutes: 120,
				timeZone: 'UTC'
			},
			autoReboot: true,
			maxParallel: 1,
			stopOnFailure: true,
			healthCheckMinutes: 5
		}
	});

	// The rollout. web-02 boots slowly: once its container is back, its agent stays off for 75
	// s – long enough for the offline alert, which the rollout's silence mutes.
	const rolloutState = () =>
		ownerSQL(
			`SELECT r.state || ':' || string_agg(n.hostname || '=' || n.state, ',' ORDER BY n.hostname)
			 FROM update_rollouts r JOIN update_rollout_nodes n ON n.rollout_id = r.id
			 WHERE r.group_id = '${groupId}' GROUP BY r.id, r.state`
		).trim();
	const container = compose(['ps', '-q', rebooting.service]).trim();
	const startedAt = () =>
		execFileSync('docker', ['inspect', '-f', '{{.State.StartedAt}}', container], {
			encoding: 'utf8'
		}).trim();
	const bootedBefore = startedAt();
	const rebootRuns = () =>
		Number(
			ownerSQL(
				`SELECT count(*) FROM node_commands WHERE node_id = '${id(rebooting.host)}' AND kind = 3 AND created_at >= '${rolloutStarted.toISOString()}'`
			).trim()
		);
	let maxActive = 0;
	let slowBoot: Promise<unknown> | undefined;
	let lastState = '';
	await expect
		.poll(
			() => {
				const state = rolloutState();
				if (state !== lastState) {
					console.log(
						`rollout after ${Math.round((Date.now() - rolloutStarted.getTime()) / 1000)} s: ${state}`
					);
					lastState = state;
				}
				// Updating, rebooting, health check (2, 3, 4) take a place of maxParallel.
				const active = (state.match(/=[234](,|$)/g) ?? []).length;
				maxActive = Math.max(maxActive, active);
				if (!slowBoot && rebootRuns() > 0) {
					slowBoot = (async () => {
						// Docker starts the container again after the reboot ended PID 1.
						while (startedAt() === bootedBefore) await new Promise((r) => setTimeout(r, 2_000));
						console.log(`${rebooting.host} rebooted; its agent starts 75 s late`);
						await new Promise((r) => setTimeout(r, 75_000));
						exec(rebooting.service, 'systemctl enable --quiet --now sermon-agent');
					})();
				}
				return state;
			},
			{ timeout: 1_500_000, intervals: [5_000] }
		)
		// Done (2) or stopped (3): a stopped one fails below with its states at once.
		.toMatch(/^[23]:/);
	await slowBoot;
	const finished = rolloutState();
	// Done (5) all but db-01, skipped (7).
	expect(finished).toBe('2:app-02=5,db-01=7,dev-01=5,web-02=5');
	expect(maxActive).toBe(1);
	expect(
		ownerSQL(
			`SELECT reason FROM update_rollout_nodes WHERE node_id = '${id(vetoing.host)}' ORDER BY since DESC LIMIT 1`
		).trim()
	).toContain('allow_remote_updates');
	// One after the other: each update starts after the one before has ended.
	const runs = ownerSQL(
		`SELECT n.hostname || ' ' || extract(epoch FROM c.created_at)::bigint || ' ' || extract(epoch FROM c.ended_at)::bigint
		 FROM node_commands c JOIN nodes n ON n.id = c.node_id
		 WHERE c.kind = 2 AND n.group_id = '${groupId}' AND c.created_at >= '${rolloutStarted.toISOString()}'
		 ORDER BY c.created_at`
	)
		.trim()
		.split('\n')
		.map((l) => l.split(' '));
	expect(runs.map((r) => r[0])).toEqual(['app-02', 'dev-01', 'web-02']);
	for (let i = 1; i < runs.length; i++) {
		expect(Number(runs[i][1]), `${runs[i][0]} after ${runs[i - 1][0]}`).toBeGreaterThanOrEqual(
			Number(runs[i - 1][2])
		);
	}
	// web-02 rebooted through the helper (one reboot run, done) and came back in a new container
	// start, with a new session.
	expect(
		ownerSQL(
			`SELECT count(*) FROM node_commands WHERE node_id = '${id(rebooting.host)}' AND kind = 3 AND state = 4 AND created_at >= '${rolloutStarted.toISOString()}'`
		).trim()
	).toBe('1');
	expect(startedAt()).not.toBe(bootedBefore);
	// Its outage fired the offline alert (critical, in its history: the instance forgets the
	// time once it resolved), muted by the rollout: no notification for the group.
	expect(
		ownerSQL(
			`SELECT count(*) FROM alert_history WHERE rule_id = 'system.node.offline' AND node_id = '${id(rebooting.host)}' AND to_state = 3 AND at >= '${rolloutStarted.toISOString()}'`
		).trim()
	).toBe('1');
	expect(
		ownerSQL(
			`SELECT count(*) FROM alert_silences WHERE comment = 'updates of the rollout (SerMon)' AND node_ids && ARRAY[${members
				.filter((m) => m !== vetoing)
				.map((m) => `'${id(m.host)}'::uuid`)
				.join(',')}]`
		).trim()
	).toBe('3');
	expect(
		ownerSQL(
			`SELECT count(*) FROM notification_deliveries WHERE NOT test AND created_at >= '${rolloutStarted.toISOString()}'
			 AND (${members.map((m) => `notification::text LIKE '%${m.host}%'`).join(' OR ')})`
		).trim()
	).toBe('0');
	// The next reports have no security update left – security-auto installs only those, the
	// ordinary sermon-e2e-extra stays; db-01 keeps both.
	await expect
		.poll(
			async () => {
				const f = await call<FleetUpdates>(
					page.request,
					base,
					'UpdateService/ListFleetUpdates',
					{}
				);
				return members.map((m) => f.nodes!.find((n) => n.hostname === m.host)?.status);
			},
			{ timeout: 300_000, intervals: [10_000] }
		)
		.toEqual([
			'UPDATE_STATUS_UPDATES',
			'UPDATE_STATUS_SECURITY',
			'UPDATE_STATUS_UPDATES',
			'UPDATE_STATUS_UPDATES'
		]);
	console.log(
		`the rollout of the group took ${Math.round((Date.now() - rolloutStarted.getTime()) / 1000)} s`
	);

	// The updates page shows the rollout with the reason of the skipped node.
	await page.goto('/updates');
	await expect(page.getByText(/db-01: skipped/).first()).toBeVisible({ timeout: 30_000 });
	expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
	await page.screenshot({ path: 'test-results/demo-rollout.png', fullPage: true });

	// db-01 lifts its veto: its admin updates it by hand in the passkeys demo.
	helperSetting(vetoing.service, 'allow_remote_updates', true);
	await call(page.request, base, 'UpdateService/RefreshUpdateInfo', { nodeId: id(vetoing.host) });
});
