import { expect, test } from '../e2e/fixtures';
import { admin, call, exec, login, ntfyMessages, ownerSQL, slackMessages } from './stack';

/**
 * The M2 demo (IMPLEMENTIERUNGSPLAN M2-27), on the installation of fleet.demo.ts: web-02 tries
 * passwords against the real sshd of web-01. The brute-force alert reaches ntfy and Slack with
 * time, user, address and port; more attempts in its window notify nobody again; it repeats while
 * nobody acknowledges it, and after the acknowledgement in the browser it stays quiet.
 */

const target = { service: 'agent-debian-12', host: 'web-01' };
const attacker = 'agent-debian-13';
// Fresh per run, so a run against an environment left running (KEEP=1) counts only its own.
const run = Date.now().toString(36);
const topic = `sermon-e2e-${run}`;
const webhook = `/services/T000/B000/${run}`;
const subject = 'SSH brute force – web-01';

/**
 * Fifteen connections in three waves of five (sshd starts dropping beyond ten unauthenticated
 * ones), one wrong password each – sshpass gives up after the first: fifteen "Failed password for
 * invalid user alice" in web-01's journal; the rule warns from ten.
 */
function attack() {
	const ssh = `sshpass -p wrong ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o PubkeyAuthentication=no -o ConnectTimeout=10 alice@${target.service} true >/dev/null 2>&1`;
	exec(attacker, `for w in 1 2 3; do for i in 1 2 3 4 5; do ${ssh} & done; wait; done; true`);
}

/** ntfy and Slack messages about the alert on web-01, without the tests of the channels. */
async function delivered() {
	const ntfy = (await ntfyMessages(topic)).filter((m) => m.title?.includes(subject));
	const slack = (await slackMessages(webhook)).filter((m) => m.text.includes(subject));
	return { ntfy, slack };
}

/** Moves the last notification of the alert six minutes back, past the 5-minute repeat. */
function ageRepeat() {
	ownerSQL(
		"UPDATE alert_routing_state SET last_sent_at = last_sent_at - interval '6 minutes' WHERE rule_id = 'security.ssh.bruteforce';"
	);
}

test('an SSH brute force on a real sshd reaches ntfy and Slack and stops repeating once acknowledged', async ({
	page,
	baseURL
}) => {
	const base = baseURL ?? '';
	await login(page, admin.email);
	exec(target.service, 'systemctl is-active ssh');
	const ip = exec(attacker, 'hostname -i').trim().split(/\s+/)[0]!;

	// Channels as an admin sets them up: ntfy inside the network, Slack by its webhook URL (here the
	// mock that answers as hooks.slack.com). "Send test" reaches both.
	const ntfy = await call<{ channel?: { id: string } }>(
		page.request,
		base,
		'NotificationChannelService/CreateChannel',
		{
			type: 'ntfy',
			name: `ntfy ${run}`,
			language: 'en',
			enabled: true,
			settings: { server: 'http://ntfy' },
			secrets: { topic }
		}
	);
	const slack = await call<{ channel?: { id: string } }>(
		page.request,
		base,
		'NotificationChannelService/CreateChannel',
		{
			type: 'slack',
			name: `Slack ${run}`,
			language: 'en',
			enabled: true,
			secrets: { url: `https://hooks.slack.com${webhook}` }
		}
	);
	const channels = [ntfy.channel!.id, slack.channel!.id];
	for (const id of channels) {
		await call(page.request, base, 'NotificationChannelService/TestChannel', { channelId: id });
	}
	expect((await ntfyMessages(topic)).some((m) => m.title?.includes('the channel works'))).toBe(
		true
	);
	expect((await slackMessages(webhook)).some((m) => m.raw.includes('the channel works'))).toBe(
		true
	);
	await call(page.request, base, 'NotificationPolicyService/CreatePolicy', {
		spec: {
			name: `SSH ${run}`,
			enabled: true,
			matchers: { rules: ['security.ssh.*'] },
			channelIds: channels,
			grouping: 'POLICY_GROUPING_NODE',
			repeatMinutes: 5
		}
	});

	// The attack: the alert shows up in the browser and in both channels with all fields.
	const started = Date.now();
	attack();
	await page.goto('/alerts');
	const alert = page
		.getByRole('article')
		.filter({ hasText: target.host })
		.filter({ hasText: 'SSH brute force' });
	await expect(alert).toContainText('Warning', { timeout: 120_000 });
	await expect(alert).toContainText(`ip: ${ip}`);
	await expect.poll(async () => (await delivered()).ntfy.length, { timeout: 60_000 }).toBe(1);
	await expect.poll(async () => (await delivered()).slack.length, { timeout: 60_000 }).toBe(1);
	console.log(
		`brute force alerted in ntfy and Slack after ${Math.round((Date.now() - started) / 1000)} s`
	);
	let { ntfy: pushed, slack: posted } = await delivered();
	expect(pushed[0]!.title).toBe(`WARNING: ${subject}`);
	for (const text of [pushed[0]!.message ?? '', posted[0]!.raw]) {
		expect(text).toContain('User: alice');
		expect(text).toContain(`Address: ${ip}`);
		expect(text).toMatch(/Port: \d+/);
		expect(text).toMatch(/Time: \d{4}-\d\d-\d\d \d\d:\d\d:\d\d UTC/);
	}
	await page.screenshot({ path: 'test-results/demo-bruteforce.png', fullPage: true });

	// More attempts in the window: still one alert, nobody is notified again.
	attack();
	await page.waitForTimeout(30_000); // three rounds of the engine
	await expect(alert).toHaveCount(1);
	expect((await delivered()).ntfy).toHaveLength(1);
	expect((await delivered()).slack).toHaveLength(1);

	// Unacknowledged it repeats: the repeat of 5 minutes, brought forward in the database.
	ageRepeat();
	await expect.poll(async () => (await delivered()).ntfy.length, { timeout: 60_000 }).toBe(2);
	await expect.poll(async () => (await delivered()).slack.length, { timeout: 60_000 }).toBe(2);
	({ ntfy: pushed } = await delivered());
	expect(pushed[1]!.message).toContain('Reminder: firing for');

	// Acknowledged in the browser: both channels hear it once, then the repeat stays away.
	await alert
		.getByRole('button', { name: `Acknowledge SSH brute force on ${target.host}` })
		.click();
	await expect(alert).toContainText(`Acknowledged by ${admin.name}`);
	await expect.poll(async () => (await delivered()).ntfy.length, { timeout: 60_000 }).toBe(3);
	await expect.poll(async () => (await delivered()).slack.length, { timeout: 60_000 }).toBe(3);
	({ ntfy: pushed, slack: posted } = await delivered());
	expect(pushed[2]!.title).toBe(`ACKNOWLEDGED: ${subject}`);
	expect(posted[2]!.raw).toContain(`Acknowledged by: ${admin.name}`);
	await page.screenshot({ path: 'test-results/demo-acknowledged.png', fullPage: true });
	ageRepeat();
	await page.waitForTimeout(30_000);
	expect((await delivered()).ntfy).toHaveLength(3);
	expect((await delivered()).slack).toHaveLength(3);
});
