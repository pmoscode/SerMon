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

class HelpState {
	/** The topic shown in the help panel, if open. */
	topic = $state<string | undefined>(undefined);
	/** The documents of the loaded language: full texts or only their summaries. */
	docs = $state<Record<string, HelpDoc>>({});
	/** The language of the full texts, if loaded. */
	locale = $state<Locale | undefined>(undefined);
	/** The language of the summaries, if loaded. */
	summaries = $state<Locale | undefined>(undefined);

	/** Loads the full documents of the language (once), for the help panel. */
	async load(locale: Locale): Promise<void> {
		if (this.locale === locale) return;
		const docs = (await loaders[locale]()).default;
		this.docs = docs;
		this.locale = locale;
		this.summaries = locale;
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
