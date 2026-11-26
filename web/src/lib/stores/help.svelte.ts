import type { HelpDoc } from '#lib/help/types.js';
import type { Locale } from '#lib/paraglide/runtime.js';

type Docs = Promise<{ default: Record<string, HelpDoc> }>;

/** One chunk per language, loaded when help is first needed. */
const loaders: Record<Locale, () => Docs> = {
	de: () => import('virtual:sermon-help/de'),
	en: () => import('virtual:sermon-help/en')
};

/** Titles and summaries only: what the ⓘ of every page needs (JavaScript budget). */
const summaryLoaders: Record<Locale, () => Docs> = {
	de: () => import('virtual:sermon-help/de/summary'),
	en: () => import('virtual:sermon-help/en/summary')
};

/** Titles only: what names the ⓘ of every page; the summaries follow on opening (M5-29). */
const titleLoaders: Record<Locale, () => Docs> = {
	de: () => import('virtual:sermon-help/de/titles'),
	en: () => import('virtual:sermon-help/en/titles')
};

class HelpState {
	/** The topic shown in the help panel, if open. */
	topic = $state<string | undefined>(undefined);
	/** The documents of the loaded language: full texts or only their summaries. */
	docs = $state<Record<string, HelpDoc>>({});
	/** The language of the full texts, if loaded. */
	locale = $state<Locale | undefined>(undefined);
	/** The language of the summaries, if loaded. */
	summaries = $state<Locale | undefined>(undefined);
	/** The language of the titles, if loaded. */
	titles = $state<Locale | undefined>(undefined);

	/** Loads the titles of the language (once), to name the ⓘ of a page. */
	async loadTitles(locale: Locale): Promise<void> {
		if (this.titles === locale || this.summaries === locale || this.locale === locale) return;
		const docs = (await titleLoaders[locale]()).default;
		// Summaries or full texts may have arrived meanwhile.
		if (this.summaries === locale || this.locale === locale) return;
		if (this.locale !== undefined) this.locale = undefined;
		if (this.summaries !== undefined) this.summaries = undefined;
		this.docs = docs;
		this.titles = locale;
	}

	/** Loads the full documents of the language (once), for the help panel. */
	async load(locale: Locale): Promise<void> {
		if (this.locale === locale) return;
		const docs = (await loaders[locale]()).default;
		this.docs = docs;
		this.locale = locale;
		this.summaries = locale;
		this.titles = locale;
	}

	/** Loads the summaries of the language (once), for tooltips. */
	async loadSummaries(locale: Locale): Promise<void> {
		if (this.summaries === locale || this.locale === locale) return;
		const docs = (await summaryLoaders[locale]()).default;
		// The full texts may have arrived meanwhile.
		if (this.locale === locale) return;
		if (this.locale !== undefined) this.locale = undefined;
		this.docs = docs;
		this.summaries = locale;
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
