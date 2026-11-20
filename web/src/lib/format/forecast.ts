import type { Locale } from '#lib/paraglide/runtime.js';

/** When something happens in the future, e.g. "in 3 days" / "in 3 Tagen" (fill forecast). */
export function formatIn(then: Date, now: Date, locale: Locale): string {
	const seconds = Math.max(0, Math.round((then.getTime() - now.getTime()) / 1000));
	const format = new Intl.RelativeTimeFormat(locale, { numeric: 'always' });
	if (seconds < 3600) return format.format(Math.max(1, Math.round(seconds / 60)), 'minute');
	if (seconds < 2 * 86400) return format.format(Math.round(seconds / 3600), 'hour');
	if (seconds < 60 * 86400) return format.format(Math.round(seconds / 86400), 'day');
	return format.format(Math.round(seconds / (30 * 86400)), 'month');
}
