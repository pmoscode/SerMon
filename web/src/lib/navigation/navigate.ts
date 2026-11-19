import { goto } from '$app/navigation';

/**
 * Navigates within the app, replacing the history entry (redirects, not user navigation).
 * The targets come from HOME, safeRedirect and loginPath: always same-origin app paths.
 */
export function redirectTo(path: string): Promise<void> {
	// eslint-disable-next-line svelte/no-navigation-without-resolve -- dynamic app paths, checked above
	return goto(path, { replaceState: true });
}
