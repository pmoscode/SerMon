import type { HelpDoc } from '#lib/help/types.js';
import type { Locale } from '#lib/paraglide/runtime.js';

/** One chunk per language, loaded when help is first needed. */
const loaders: Record<Locale, () => Promise<{ default: Record<string, HelpDoc> }>> = {
	de: () => import('virtual:sermon-help/de'),
	en: () => import('virtual:sermon-help/en')
};

class HelpState {
	/** The topic shown in the help panel, if open. */
	topic = $state<string | undefined>(undefined);
	/** The documents of the loaded language. */
	docs = $state<Record<string, HelpDoc>>({});
	locale = $state<Locale | undefined>(undefined);

	/** Loads the documents of the language (once). */
	async load(locale: Locale): Promise<void> {
		if (this.locale === locale) return;
		const docs = (await loaders[locale]()).default;
		this.docs = docs;
		this.locale = locale;
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
