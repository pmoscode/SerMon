import type { HelpDoc } from '#lib/help/types.js';
import type { Locale } from '#lib/paraglide/runtime.js';

type Docs = Promise<{ default: Record<string, HelpDoc> }>;

/**
 * The full texts of each language, loaded when help is first needed: in a build a JSON file
 * rather than a chunk – they are data, not code (scripts/help.js).
 */
const loaders: Record<Locale, () => Promise<{ default: Promise<Record<string, HelpDoc>> }>> = {
	de: () => import('virtual:sermon-help/de'),
	en: () => import('virtual:sermon-help/en')
};

/**
 * Titles only: what names the ⓘ of every page (JavaScript budget); the full texts follow with
 * the first opening of a popover or the panel (M5-29).
 */
const titleLoaders: Record<Locale, () => Docs> = {
	de: () => import('virtual:sermon-help/de/titles'),
	en: () => import('virtual:sermon-help/en/titles')
};

class HelpState {
	/** The topic shown in the help panel, if open. */
	topic = $state<string | undefined>(undefined);
	/** The documents of the loaded language: full texts or only their titles. */
	docs = $state<Record<string, HelpDoc>>({});
	/** The language of the full texts, if loaded. */
	locale = $state<Locale | undefined>(undefined);
	/** The language of the titles, if loaded. */
	titles = $state<Locale | undefined>(undefined);

	/** Loads the titles of the language (once), to name the ⓘ of a page. */
	async loadTitles(locale: Locale): Promise<void> {
		if (this.titles === locale || this.locale === locale) return;
		const docs = (await titleLoaders[locale]()).default;
		// The full texts may have arrived meanwhile.
		if (this.locale === locale) return;
		if (this.locale !== undefined) this.locale = undefined;
		this.docs = docs;
		this.titles = locale;
	}

	/** Loads the full documents of the language (once), for popovers and the help panel. */
	async load(locale: Locale): Promise<void> {
		if (this.locale === locale) return;
		const docs = await (await loaders[locale]()).default;
		this.docs = docs;
		this.locale = locale;
		this.titles = locale;
	}

	get(topic: string): HelpDoc | undefined {
		return this.docs[topic];
	}

	open(topic: string): void {
		this.topic = topic;
	}

	close(): void {
		this.topic = undefined;
	}
}

export const helpState = new HelpState();
