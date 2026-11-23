/** Help documents built from docs/help/<locale>/**.md (scripts/help.js). */
export type Inline =
	| { type: 'text'; text: string }
	| { type: 'strong'; content: Inline[] }
	| { type: 'em'; content: Inline[] }
	| { type: 'code'; text: string }
	| { type: 'link'; href: string; content: Inline[] };

export type Block =
	| { type: 'heading'; level: 2 | 3; content: Inline[] }
	| { type: 'paragraph'; content: Inline[] }
	| { type: 'list'; ordered: boolean; items: Inline[][] }
	| { type: 'code'; text: string };

export interface HelpDoc {
	title: string;
	/** The first paragraph, shown in tooltips. */
	summary: Inline[];
	body: Block[];
	/** Set in the summaries: the full text has a body (loaded with the panel). */
	more?: boolean;
}
