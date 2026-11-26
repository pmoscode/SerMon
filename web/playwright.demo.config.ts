import { defineConfig, devices } from '@playwright/test';

/**
 * End-to-end demo against the real stack of deploy/e2e (M1-33): started by scripts/e2e-demo.sh,
 * which sets E2E_BASE_URL, E2E_DIR and COMPOSE_FILE. Runs serially: one installation, one story.
 * FULL=1 adds what takes long (M4-28): the anomalies, the rollout of a policy and the budget during
 * a listing – nightly and before a release; without it the fast demo for every change.
 */
const full = process.env.FULL === '1';

export default defineConfig({
	testDir: 'tests/demo',
	testMatch: '**/*.demo.ts',
	fullyParallel: false,
	workers: 1,
	forbidOnly: !!process.env.CI,
	retries: 0,
	timeout: 6 * 60_000,
	reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
	use: {
		baseURL: process.env.E2E_BASE_URL ?? 'https://localhost:18443',
		// Caddy's own CA (`tls internal`), like a self-hosted server without a public certificate.
		ignoreHTTPSErrors: true,
		trace: 'retain-on-failure',
		screenshot: 'only-on-failure'
	},
	// The installation first; the alerts of M2-27 build on its hosts and its admin.
	projects: [
		{ name: 'install', testMatch: 'fleet.demo.ts', use: { ...devices['Desktop Chrome'] } },
		{
			name: 'alerts',
			testMatch: 'alerts.demo.ts',
			dependencies: ['install'],
			use: { ...devices['Desktop Chrome'] }
		},
		// Dex (M3-22) answers as dex.test on 127.0.0.1:18556, the issuer the server knows too.
		{
			name: 'sso',
			testMatch: 'sso.demo.ts',
			dependencies: ['install'],
			use: {
				...devices['Desktop Chrome'],
				launchOptions: { args: ['--host-resolver-rules=MAP dex.test 127.0.0.1'] }
			}
		},
		// The M3 demo (M3-26): troubleshooting without SSH, after the attack of the alerts.
		{
			name: 'troubleshoot',
			testMatch: 'troubleshoot.demo.ts',
			dependencies: ['alerts'],
			use: { ...devices['Desktop Chrome'] }
		},
		// The M4 demo (M4-28, FULL=1): anomalies first – they restart the server –, then the rollout
		// of a policy, whose notification policy needs the channels of the alerts.
		...(full
			? [
					{
						name: 'anomaly',
						testMatch: 'anomaly.demo.ts',
						dependencies: ['troubleshoot'],
						use: { ...devices['Desktop Chrome'] }
					},
					{
						name: 'patch',
						testMatch: 'patch.demo.ts',
						dependencies: ['anomaly'],
						use: { ...devices['Desktop Chrome'] }
					}
				]
			: []),
		// Passkeys (M3-24) last: with one the admin's password sign-in would need it as second factor.
		// They also update db-01 by hand – with FULL=1 after its veto in the rollout (M4-28).
		{
			name: 'passkeys',
			testMatch: 'passkeys.demo.ts',
			dependencies: ['alerts', 'sso', 'troubleshoot', ...(full ? ['patch'] : [])],
			use: { ...devices['Desktop Chrome'] }
		}
	]
});
