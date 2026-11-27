import AxeBuilder from '@axe-core/playwright';
import { installed, mockApi, session } from './api';
import { expect, test } from './fixtures';

const day = 24 * 60 * 60 * 1000;
const iso = (offsetDays: number) => new Date(Date.now() + offsetDays * day).toISOString();
const stepUp = {
	status: 400,
	json: {
		code: 'failed_precondition',
		message: 'please confirm your identity again',
		details: [{ type: 'sermon.web.v1.StepUpRequired', value: '' }]
	}
};

// V1-19: the superadmin sees the agent CA expire and rolls it over with a step-up.
test.describe('agent CA (mocked API)', () => {
	test.use({ expectedHttpErrors: [400] });

	test('warns before the expiry, starts the rollover and retires the previous CA', async ({
		page
	}) => {
		let confirmed = false;
		const calls: string[] = [];
		const old = {
			id: 1,
			issuing: true,
			createTime: iso(-670),
			expireTime: iso(60.5),
			rootFingerprint: 'a1'.repeat(32),
			certificatesInUse: '12'
		};
		let authorities: object[] = [old];
		const superadmin = {
			...session,
			superadmin: true,
			instanceProblems: [
				{ check: 'agent_ca_expiry', level: 'warning', since: iso(-30), value: 60.5 }
			]
		};
		await mockApi(page, {
			...installed(true),
			'AuthService/GetSession': () => ({ json: { session: superadmin } }),
			'AgentCAService/GetAgentCAStatus': () => ({ json: { authorities } }),
			'AgentCAService/StartAgentCARollover': () => {
				if (!confirmed) return stepUp;
				calls.push('start');
				authorities = [
					{ ...old, issuing: false, certificatesInUse: '0' },
					{
						id: 2,
						issuing: true,
						createTime: iso(0),
						expireTime: iso(730),
						rootFingerprint: 'b2'.repeat(32),
						certificatesInUse: '0'
					}
				];
				return { json: {} };
			},
			'AgentCAService/RetireAgentCA': (body) => {
				calls.push(`retire ${(body as { id: number }).id}`);
				return { json: {} };
			},
			'AuthService/ConfirmPassword': () => {
				confirmed = true;
				return { json: { session: superadmin } };
			}
		});
		await page.goto('/fleet');
		await expect(
			page.getByText('The agent CA expires in 60 days – a rollover is due')
		).toBeVisible();
		await page.getByRole('link', { name: 'Open Agent CA' }).click();
		await expect(page.getByRole('heading', { name: 'Agent CA', level: 1 })).toBeVisible();
		const tabs = page.getByRole('navigation', { name: 'Pages of the instance' });
		await expect(tabs.getByRole('link', { name: 'Agent CA' })).toHaveAttribute(
			'aria-current',
			'page'
		);
		await expect(
			page
				.getByRole('navigation', { name: 'Main navigation' })
				.getByRole('link', { name: 'Organizations' })
		).toHaveAttribute('aria-current', 'page');
		await expect(page.getByText(/in 60 days\. After that agents and server refuse/)).toBeVisible();
		await expect(page.getByRole('row', { name: /Issuing/ })).toContainText('12');
		let results = await new AxeBuilder({ page }).analyze();
		expect(results.violations).toEqual([]);
		await page.screenshot({ path: 'test-results/agent-ca.png', fullPage: true });

		await page.getByRole('button', { name: 'Start the rollover' }).click();
		await page.getByRole('button', { name: 'Start the rollover' }).click();
		const dialog = page.getByRole('dialog');
		await dialog.getByLabel('Password').fill('correct horse battery');
		await dialog.getByRole('button', { name: 'Confirm' }).click();
		await expect(page.getByText(/The rollover is running/)).toBeVisible();
		await expect(page.getByRole('row', { name: /Previous, still trusted/ })).toBeVisible();
		results = await new AxeBuilder({ page }).analyze();
		expect(results.violations).toEqual([]);

		await page.getByRole('button', { name: 'Retire the previous CA' }).click();
		await page.getByRole('button', { name: 'Retire the previous CA' }).click();
		await expect(page.getByText(/The previous CA is retired/)).toBeVisible();
		expect(calls).toEqual(['start', 'retire 1']);
	});
});
