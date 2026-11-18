import { getLocale, locales, setLocale, type Locale } from '#lib/paraglide/runtime.js';

/** Native names of the supported languages – shown in that language regardless of the UI language. */
export const localeNames: Record<Locale, string> = { en: 'English', de: 'Deutsch' };

export { locales, type Locale };

class LocaleState {
	/** The active UI language; components re-render when it changes. */
	current = $state<Locale>(getLocale());
}

export const localeState = new LocaleState();

/** Switches the UI language without a page reload and remembers the choice. */
export function switchLocale(locale: Locale): void {
	setLocale(locale, { reload: false });
	localeState.current = locale;
	document.documentElement.lang = locale;
}
