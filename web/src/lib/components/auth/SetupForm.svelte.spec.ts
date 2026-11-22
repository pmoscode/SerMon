import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { Code } from '#lib/api/client.js';
import { onboardingApi } from '#lib/api/onboarding.js';
import { switchLocale } from '#lib/i18n/locale.svelte.js';
import { m } from '#lib/paraglide/messages.js';
import { PASSPHRASE, rpcError } from '#lib/test/fakes.js';
import SetupForm from './SetupForm.svelte';

const tooShort = 'short';
const mismatch = 'other';

const valid = {
	'Setup token': ' tok-123 ',
	'Organization name': 'Acme',
	Name: 'Mia',
	'Email address': 'mia@example.org',
	Password: PASSPHRASE,
	'Repeat password': PASSPHRASE
};

async function fill(values: Record<string, string>) {
	for (const [label, value] of Object.entries(values)) {
		await page.getByLabelText(label, { exact: true }).fill(value);
	}
	await page.getByRole('button', { name: 'Set up' }).click();
}

describe('SetupForm', () => {
	beforeEach(() => switchLocale('en'));
	afterEach(() => vi.restoreAllMocks());

	it('checks the fields before it calls the server', async () => {
		const complete = vi.spyOn(onboardingApi.setup, 'completeSetup');
		render(SetupForm, { onDone: vi.fn() });

		await fill({
			...valid,
			'Email address': 'mia',
			Password: tooShort,
			'Repeat password': mismatch
		});

		await expect.element(page.getByText('Please enter a valid email address.')).toBeVisible();
		await expect.element(page.getByText('At least 12 characters.')).toBeVisible();
		await expect.element(page.getByText("The passwords don't match.")).toBeVisible();
		await expect
			.element(page.getByLabelText('Password', { exact: true }))
			.toHaveAttribute('aria-invalid', 'true');
		expect(complete).not.toHaveBeenCalled();
	});

	it('sends the trimmed values and reports success', async () => {
		const complete = vi.spyOn(onboardingApi.setup, 'completeSetup').mockResolvedValue({} as never);
		const onDone = vi.fn();
		render(SetupForm, { onDone });

		await fill(valid);

		await vi.waitFor(() => expect(onDone).toHaveBeenCalledOnce());
		expect(complete.mock.calls[0][0]).toEqual({
			token: 'tok-123',
			organizationName: 'Acme',
			displayName: 'Mia',
			email: 'mia@example.org',
			password: PASSPHRASE
		});
	});

	it.each([
		[Code.Unauthenticated, m.setup_error_token],
		[Code.InvalidArgument, m.setup_error_invalid],
		[Code.Internal, m.error_unavailable]
	])('shows the server failure %s', async (code, text) => {
		vi.spyOn(onboardingApi.setup, 'completeSetup').mockRejectedValue(rpcError(code));
		const onDone = vi.fn();
		render(SetupForm, { onDone });

		await fill(valid);

		await expect.element(page.getByRole('alert')).toHaveTextContent(text());
		expect(onDone).not.toHaveBeenCalled();
	});
});
