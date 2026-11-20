/** Where the app starts after login. */
export const HOME = '/fleet';

/**
 * The target of a ?redirect= parameter, only if it is a path of this app: anything else
 * (other hosts, protocol-relative "//host", "/\host", schemes) falls back to HOME – no open redirect.
 */
export function safeRedirect(target: string | null | undefined): string {
	if (!target || !target.startsWith('/') || isProtocolRelative(target)) return HOME;
	try {
		const url = new URL(target, 'http://sermon.invalid');
		if (url.origin !== 'http://sermon.invalid' || url.pathname.startsWith('/login')) return HOME;
		// Normalization can turn "/.//host" into "//host": check the result, too.
		if (isProtocolRelative(url.pathname)) return HOME;
		return url.pathname + url.search + url.hash;
	} catch {
		return HOME;
	}
}

function isProtocolRelative(path: string): boolean {
	return path.startsWith('//') || path.startsWith('/\\');
}

/** The login page that returns to the given path afterwards. */
export function loginPath(
	reason: 'expired' | 'logged_out' | 'setup_done' | 'invited' | undefined,
	from?: string
): string {
	const params = new URLSearchParams();
	if (reason) params.set('reason', reason);
	if (from && safeRedirect(from) !== HOME) params.set('redirect', safeRedirect(from));
	const query = params.toString();
	return query ? `/login?${query}` : '/login';
}
