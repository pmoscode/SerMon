import { expect, test } from '../e2e/fixtures';
import { admin, call, compose, execAsync, login, ownerSQL } from './stack';

/**
 * Anomalies against the real stack (M4-28): web-01 gets four weeks of history in the database,
 * drawn from its own minutes so far – only here, in the E2E environment. After a restart the
 * server's worker learns the hours of the week from it and warms up; five minutes of load on the
 * host become an anomaly band in the CPU chart and the top of "What was unusual?".
 */

const node = { service: 'agent-debian-12', host: 'web-01' };

test('a load spike against four weeks of history shows as anomaly band and on top of what was unusual', async ({
	page,
	baseURL
}) => {
	test.setTimeout(20 * 60_000);
	const base = baseURL ?? '';
	await login(page, admin.email);
	const fleet = await call<{ nodes?: { nodeId: string; hostname: string }[] }>(
		page.request,
		base,
		'FleetService/GetFleetSummary',
		{}
	);
	const nodeId = fleet.nodes!.find((n) => n.hostname === node.host)!.nodeId;

	// The history: per CPU mode one value a minute from 28 days and 7 hours ago up to the node's
	// first real minute, each drawn from its real values (their usual range is the host's). The
	// node and its series count as learnt.
	const series = `SELECT id FROM series WHERE node_id = '${nodeId}' AND metric = 'sermon_cpu_utilization_ratio'
		AND labels->>'mode' IN ('user', 'system', 'iowait', 'steal')`;
	ownerSQL(`
		INSERT INTO metrics_raw (ts, series_id, avg, min, max)
		SELECT t, r.id, v.x, v.x, v.x
		FROM (SELECT s.id, array_agg(m.avg) AS vals, min(m.ts) AS first
		      FROM metrics_raw m JOIN series s ON s.id = m.series_id WHERE s.id IN (${series}) GROUP BY s.id) r,
		     generate_series(date_trunc('minute', now() - interval '28 days 7 hours'), r.first - interval '1 minute', interval '1 minute') t,
		     LATERAL (SELECT r.vals[1 + floor(random() * cardinality(r.vals))::int + 0 * extract(epoch FROM t)::int] AS x) v;
		UPDATE nodes SET learning_since = now() - interval '30 days' WHERE id = '${nodeId}';
		UPDATE series SET created_at = now() - interval '30 days' WHERE node_id = '${nodeId}';`);
	ownerSQL(`CALL refresh_continuous_aggregate('metrics_1m', NULL, date_trunc('minute', now()));`);
	const rows = ownerSQL(
		`SELECT count(*) FROM metrics_1m WHERE series_id IN (${series}) AND bucket < now() - interval '27 days'`
	).trim();
	expect(Number(rows)).toBeGreaterThan(4 * 60);

	// The worker warms up when it takes the organization over: a restart of the server, as after
	// an upgrade. Its first round learns the hours the warm-up needs (M4-28: those first).
	// The page first leaves the app: its requests would meet the restart (502).
	await page.goto('about:blank');
	compose(['restart', 'server']);
	await expect
		.poll(async () => (await page.request.get(`${base}/healthz`)).status(), {
			timeout: 120_000,
			intervals: [2_000]
		})
		.toBe(200);
	await expect
		.poll(
			() =>
				Number(
					ownerSQL(
						`SELECT count(*) FROM anomaly_baselines WHERE series_id IN (${series}) AND points >= 30`
					).trim()
				),
			{ timeout: 180_000, intervals: [5_000] }
		)
		.toBeGreaterThanOrEqual(4 * 7);
	console.log(`the worker learnt the warm-up's hours of ${node.host}`);

	// Three minutes of load on every core, enough for the debounce (the containers share the
	// kernel, so every node sees it; only web-01 has history). Averaged over 10 minutes the CPU
	// stays below its alert.
	const spikeStarted = Date.now();
	const load = execAsync(
		node.service,
		'for i in $(seq $(nproc)); do timeout 180 sh -c "while :; do :; done" & done; wait; true'
	);
	type Band = {
		learningDay?: number;
		series?: { metric: string; labels?: Record<string, string>; intervals?: { from: string }[] }[];
	};
	let band: Band = {};
	// Should the band not come: what the worker had to go by, for the log.
	const diagnose = () => {
		const report = (title: string, sql: string) =>
			console.log(`${title}:\n${ownerSQL(sql).trim() || '(none)'}`);
		const user = `${series} AND labels->>'mode' = 'user'`;
		report(
			'baselines of the current hour (mode, median, mad, low, high, points, computed)',
			`SELECT s.labels->>'mode', round(b.median), round(b.mad), round(b.low), round(b.high), b.points, b.computed_at
			 FROM anomaly_baselines b JOIN series s ON s.id = b.series_id
			 WHERE b.series_id IN (${series}) AND b.hour = (extract(isodow FROM now() AT TIME ZONE 'UTC')::int - 1) * 24 + extract(hour FROM now() AT TIME ZONE 'UTC')::int`
		);
		report(
			'mode user in metrics_1m over the last 15 minutes',
			`SELECT to_char(bucket, 'HH24:MI'), round(avg) FROM metrics_1m WHERE series_id IN (${user}) AND bucket > now() - interval '15 minutes' ORDER BY bucket`
		);
		report(
			'notable minutes since the spike (mode, minutes, anomalous, max score)',
			`SELECT s.labels->>'mode', count(*), count(*) FILTER (WHERE p.anomalous), round(max(p.score)::numeric, 1)
			 FROM anomaly_points p JOIN series s ON s.id = p.series_id
			 WHERE p.series_id IN (${series}) AND p.ts > now() - interval '30 minutes' GROUP BY 1`
		);
		report(
			'anomaly rate of the node, last minutes written',
			`SELECT to_char(max(r.ts), 'HH24:MI:SS'), count(*) FROM metrics_raw r JOIN series s ON s.id = r.series_id
			 WHERE s.node_id = '${nodeId}' AND s.metric = 'sermon_anomaly_rate_ratio' AND r.ts > now() - interval '30 minutes'`
		);
		report('lease', `SELECT holder, expires_at FROM anomaly_leases`);
	};
	await expect
		.poll(
			async () => {
				band = await call<Band>(page.request, base, 'AnomalyService/GetNodeAnomalies', {
					nodeId,
					from: new Date(spikeStarted - 10 * 60_000).toISOString(),
					to: new Date().toISOString()
				});
				return (band.series ?? []).some(
					(s) => s.labels?.mode === 'user' && (s.intervals ?? []).length > 0
				);
			},
			{ timeout: 600_000, intervals: [10_000] }
		)
		.toBe(true)
		.catch((e: unknown) => {
			diagnose();
			throw e;
		});
	expect(band.learningDay ?? 0).toBe(0);
	const user = band.series!.find((s) => s.labels?.mode === 'user')!;
	expect(Date.parse(user.intervals![0].from)).toBeGreaterThanOrEqual(spikeStarted - 60_000);
	console.log(
		`the spike on ${node.host} was anomalous after ${Math.round((Date.now() - spikeStarted) / 1000)} s`
	);

	// In the UI: the CPU chart names the band; "What was unusual?" over the last hour has the
	// CPU user mode on top.
	await page.goto(`/nodes/${nodeId}`);
	const cpu = page.getByRole('figure', { name: 'CPU by mode' });
	await expect(cpu.getByText('unusual', { exact: true })).toBeVisible({ timeout: 30_000 });
	await page.getByRole('button', { name: 'What was unusual?' }).click();
	const unusual = page.getByRole('table', { name: 'What was unusual?' });
	await expect(unusual.getByRole('row').nth(1)).toContainText('mode=user', { timeout: 30_000 });
	await page.screenshot({ path: 'test-results/demo-anomaly.png', fullPage: true });
	// The fleet's view lists web-01 with its rate.
	await page.goto('/anomalies');
	await expect(
		page.getByRole('table', { name: 'Nodes by anomaly rate' }).getByRole('row', { name: /web-01/ })
	).toBeVisible({ timeout: 60_000 });
	await page.screenshot({ path: 'test-results/demo-anomalies.png', fullPage: true });
	await load;
});
