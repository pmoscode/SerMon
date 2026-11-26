/**
 * The address of a display (ADR 0040): `/kiosk#t=<token>,<token>&rotate=60`. The tokens stay in
 * the fragment, which the browser never sends to the server, a proxy or a referrer.
 */

export const TOKEN = /^smk_[A-Za-z0-9_-]{43}$/;
export const MAX_BOARDS = 10;
export const DEFAULT_ROTATE = 60;

export interface KioskConfig {
	tokens: string[];
	/** Seconds per dashboard while there are several (10 to 3600). */
	rotate: number;
}

export function parseFragment(hash: string): KioskConfig {
	const params = new URLSearchParams(hash.replace(/^#/, ''));
	const tokens = [
		...new Set(
			(params.get('t') ?? '')
				.split(',')
				.map((t) => t.trim())
				.filter((t) => TOKEN.test(t))
		)
	].slice(0, MAX_BOARDS);
	const seconds = Number(params.get('rotate'));
	const rotate =
		Number.isFinite(seconds) && seconds >= 10
			? Math.min(Math.round(seconds), 3600)
			: DEFAULT_ROTATE;
	return { tokens, rotate };
}

/** The address of a display for the tokens. */
export function kioskUrl(origin: string, tokens: string[], rotate = DEFAULT_ROTATE): string {
	const params = new URLSearchParams({ t: tokens.join(',') });
	if (rotate !== DEFAULT_ROTATE) params.set('rotate', String(rotate));
	return `${origin}/kiosk#${params.toString().replaceAll('%2C', ',')}`;
}
