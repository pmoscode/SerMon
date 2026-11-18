import { defineConfig, devices } from '@playwright/test';

const port = 4173;

export default defineConfig({
	testDir: 'tests/e2e',
	testMatch: '**/*.e2e.ts',
	fullyParallel: true,
	forbidOnly: !!process.env.CI,
	retries: process.env.CI ? 1 : 0,
	use: {
		baseURL: `http://127.0.0.1:${port}`,
		trace: 'retain-on-failure'
	},
	projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
	webServer: {
		// Production setup: the UI is embedded into sermon-server, which also sets CSP and security headers.
		command: 'cd .. && just build-web && cd server && go run ./cmd/sermon-server',
		url: `http://127.0.0.1:${port}/healthz`,
		env: { SERMON_HTTP_ADDR: `127.0.0.1:${port}`, SERMON_LOG_FORMAT: 'text' },
		reuseExistingServer: !process.env.CI,
		timeout: 180_000
	}
});
