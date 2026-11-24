import { defineConfig, devices } from '@playwright/test';

/**
 * End-to-end demo against the real stack of deploy/e2e (M1-33): started by scripts/e2e-demo.sh,
 * which sets E2E_BASE_URL, E2E_DIR and COMPOSE_FILE. Runs serially: one installation, one story.
 */
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
		}
	]
});
