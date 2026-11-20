// Help pipeline (KONZEPT §17): docs/help/<locale>/**/*.md → structured documents for the UI.
//
// The Markdown is turned into a small tree (blocks and inlines), never into HTML: the UI renders
// it as text nodes, so help texts can't inject markup ({@html} stays banned).
//
// Format of a help file:
//   # Title
//
//   Summary paragraph – shown in the ⓘ tooltip.
//
//   ## Further sections, paragraphs, lists (- or 1.), ``` code blocks, **bold**, *italic*,
//   `code` and links to other topics [text](help:metrics/…), app paths [text](/fleet) or https.
//
// Usage: node scripts/help.js lint <helpDir> <srcDir> – fails on missing translations, broken
// files and topics used in the source that don't exist.
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative, sep } from 'node:path';
import { argv, exit } from 'node:process';
import { fileURLToPath } from 'node:url';

/**
 * @typedef {{ type: 'text', text: string }
 *   | { type: 'strong' | 'em', content: Inline[] }
 *   | { type: 'code', text: string }
 *   | { type: 'link', href: string, content: Inline[] }} Inline
 * @typedef {{ type: 'heading', level: 2 | 3, content: Inline[] }
 *   | { type: 'paragraph', content: Inline[] }
 *   | { type: 'list', ordered: boolean, items: Inline[][] }
 *   | { type: 'code', text: string }} Block
 * @typedef {{ title: string, summary: Inline[], body: Block[] }} HelpDoc
 */

export const locales = ['de', 'en'];

/** Topic keys: lower-case path segments, e.g. "metrics/sermon_load_average". */
export const topicPattern = /^[a-z0-9_-]+(?:\/[a-z0-9_-]+)*$/;

const linkTarget = /^(?:help:[a-z0-9_/-]+|\/[^/\\][^\s]*|\/|https:\/\/[^\s]+)$/;

/**
 * Parses inline Markdown: **strong**, *em* or _em_, `code`, [text](href).
 * @param {string} text
 * @param {string[]} problems
 * @returns {Inline[]}
 */
export function parseInline(text, problems = []) {
	/** @type {Inline[]} */
	const out = [];
	let plain = '';
	const flush = () => {
		if (plain) out.push({ type: 'text', text: plain });
		plain = '';
	};
	let i = 0;
	while (i < text.length) {
		const rest = text.slice(i);
		let match;
		if ((match = /^`([^`]+)`/.exec(rest))) {
			flush();
			out.push({ type: 'code', text: match[1] });
		} else if ((match = /^\*\*(.+?)\*\*/.exec(rest))) {
			flush();
			out.push({ type: 'strong', content: parseInline(match[1], problems) });
		} else if ((match = /^(?:\*([^*\s][^*]*?)\*|_([^_\s][^_]*?)_(?![a-z0-9]))/i.exec(rest))) {
			flush();
			out.push({ type: 'em', content: parseInline(match[1] ?? match[2], problems) });
		} else if ((match = /^\[([^\]]+)\]\(([^)\s]+)\)/.exec(rest))) {
			flush();
			const href = match[2];
			const content = parseInline(match[1], problems);
			if (linkTarget.test(href)) {
				out.push({ type: 'link', href, content });
			} else {
				// Only the text stays: the target never reaches the bundle.
				problems.push(`link target "${href}" not allowed (help:, /path or https://)`);
				out.push(...content);
			}
		} else {
			plain += text[i];
			i++;
			continue;
		}
		i += match[0].length;
	}
	flush();
	return out;
}

/**
 * Parses a help file.
 * @param {string} markdown
 * @returns {{ doc: HelpDoc, problems: string[] }}
 */
export function parseHelp(markdown) {
	/** @type {string[]} */
	const problems = [];
	/** @type {Block[]} */
	const blocks = [];
	let title = '';
	const lines = markdown.replace(/\r\n?/g, '\n').split('\n');
	let i = 0;
	/** @type {string[]} */
	let paragraph = [];
	/** @type {{ ordered: boolean, items: string[] } | undefined} */
	let list;

	const endParagraph = () => {
		if (paragraph.length)
			blocks.push({ type: 'paragraph', content: parseInline(paragraph.join(' '), problems) });
		paragraph = [];
	};
	const endList = () => {
		if (list) {
			blocks.push({
				type: 'list',
				ordered: list.ordered,
				items: list.items.map((item) => parseInline(item, problems))
			});
		}
		list = undefined;
	};

	while (i < lines.length) {
		const line = lines[i];
		const trimmed = line.trim();
		let match;
		if (trimmed.startsWith('```')) {
			endParagraph();
			endList();
			const code = [];
			i++;
			while (i < lines.length && !lines[i].trim().startsWith('```')) code.push(lines[i++]);
			if (i >= lines.length) problems.push('code block not closed');
			blocks.push({ type: 'code', text: code.join('\n') });
		} else if ((match = /^(#{1,6})\s+(.+)$/.exec(trimmed))) {
			endParagraph();
			endList();
			const level = match[1].length;
			if (level === 1) {
				if (title || blocks.length)
					problems.push('the title (# …) must be the first line and appear once');
				title = match[2].trim();
			} else if (level <= 3) {
				blocks.push({
					type: 'heading',
					level: /** @type {2 | 3} */ (level),
					content: parseInline(match[2], problems)
				});
			} else {
				problems.push(`heading level ${level} not supported (use ## or ###)`);
			}
		} else if ((match = /^(?:([-*])|(\d+)\.)\s+(.*)$/.exec(trimmed))) {
			endParagraph();
			const ordered = match[2] !== undefined;
			if (list && list.ordered !== ordered) endList();
			list ??= { ordered, items: [] };
			list.items.push(match[3]);
		} else if (!trimmed) {
			endParagraph();
			endList();
		} else if (list && /^\s+/.test(line)) {
			list.items[list.items.length - 1] += ' ' + trimmed;
		} else {
			endList();
			if (/^\|/.test(trimmed) || /^<\/?[a-z]/i.test(trimmed)) {
				problems.push(`unsupported Markdown (tables, HTML): "${trimmed.slice(0, 40)}"`);
			}
			paragraph.push(trimmed);
		}
		i++;
	}
	endParagraph();
	endList();

	if (!title) problems.push('missing title (# …) in the first line');
	const first = blocks[0];
	if (!first || first.type !== 'paragraph')
		problems.push('missing summary paragraph after the title');
	const summary = first?.type === 'paragraph' ? first.content : [];
	return {
		doc: { title, summary, body: first?.type === 'paragraph' ? blocks.slice(1) : blocks },
		problems
	};
}

/** @param {string} dir @returns {string[]} */
function markdownFiles(dir) {
	/** @type {string[]} */
	let files = [];
	let entries;
	try {
		entries = readdirSync(dir, { withFileTypes: true });
	} catch {
		return files;
	}
	for (const entry of entries) {
		const path = join(dir, entry.name);
		if (entry.isDirectory()) files = files.concat(markdownFiles(path));
		else if (entry.name.endsWith('.md')) files.push(path);
	}
	return files;
}

/**
 * Loads all help files of all locales.
 * @param {string} root the docs/help directory
 * @returns {{ docs: Record<string, Record<string, HelpDoc>>, files: string[], problems: string[] }}
 */
export function loadHelp(root) {
	/** @type {Record<string, Record<string, HelpDoc>>} */
	const docs = {};
	/** @type {string[]} */
	const problems = [];
	/** @type {string[]} */
	const files = [];
	for (const locale of locales) {
		docs[locale] = {};
		for (const file of markdownFiles(join(root, locale))) {
			files.push(file);
			const topic = relative(join(root, locale), file).slice(0, -'.md'.length).split(sep).join('/');
			const name = `${locale}/${topic}.md`;
			if (!topicPattern.test(topic)) {
				problems.push(`${name}: file name must be lower-case letters, digits, _ and -`);
				continue;
			}
			const parsed = parseHelp(readFileSync(file, 'utf8'));
			for (const problem of parsed.problems) problems.push(`${name}: ${problem}`);
			docs[locale][topic] = parsed.doc;
		}
	}
	return { docs, files, problems };
}

/** @param {Inline[]} content @returns {string[]} */
function helpLinks(content) {
	return content.flatMap((inline) => {
		if (inline.type === 'link') return [inline.href, ...helpLinks(inline.content)];
		if (inline.type === 'strong' || inline.type === 'em') return helpLinks(inline.content);
		return [];
	});
}

/** @param {HelpDoc} doc @returns {string[]} */
function linkedTopics(doc) {
	const inlines = [doc.summary];
	for (const block of doc.body) {
		if (block.type === 'paragraph' || block.type === 'heading') inlines.push(block.content);
		if (block.type === 'list') inlines.push(...block.items);
	}
	return inlines
		.flatMap(helpLinks)
		.filter((href) => href.startsWith('help:'))
		.map((href) => href.slice('help:'.length));
}

/** Topic literals in the UI source: strings like 'pages/fleet' or "figures/cpu_busy". */
const usedTopic =
	/['"`]((?:pages|figures|metrics|concepts|modules|channels|rules)\/[a-z0-9_/-]+)['"`]/g;

/** @param {string} dir @returns {string[]} */
function sourceFiles(dir) {
	/** @type {string[]} */
	let files = [];
	for (const entry of readdirSync(dir, { withFileTypes: true })) {
		const path = join(dir, entry.name);
		if (entry.isDirectory()) {
			if (!['paraglide', 'gen'].includes(entry.name)) files = files.concat(sourceFiles(path));
		} else if (
			/\.(svelte|ts|js)$/.test(entry.name) &&
			!/\.(test|spec)\.(ts|js)$/.test(entry.name)
		) {
			files.push(path);
		}
	}
	return files;
}

/**
 * Checks the help texts: every topic in every locale, no broken files, every linked or used
 * topic exists.
 * @param {string} root the docs/help directory
 * @param {string} [src] the UI source directory to check used topics
 * @returns {string[]} problems
 */
export function lintHelp(root, src) {
	const { docs, problems } = loadHelp(root);
	const topics = new Set(locales.flatMap((locale) => Object.keys(docs[locale])));
	for (const topic of [...topics].sort()) {
		for (const locale of locales) {
			if (!docs[locale][topic])
				problems.push(`${locale}/${topic}.md: missing (exists in another language)`);
		}
	}
	for (const locale of locales) {
		for (const [topic, doc] of Object.entries(docs[locale])) {
			for (const linked of linkedTopics(doc)) {
				if (!topics.has(linked))
					problems.push(`${locale}/${topic}.md: links to unknown topic "${linked}"`);
			}
		}
	}
	if (src) {
		for (const file of sourceFiles(src)) {
			for (const [, topic] of readFileSync(file, 'utf8').matchAll(usedTopic)) {
				if (!topics.has(topic))
					problems.push(`${relative(src, file)}: uses unknown help topic "${topic}"`);
			}
		}
	}
	return problems;
}

const virtualPrefix = 'virtual:sermon-help/';

/**
 * Vite plugin: `import docs from 'virtual:sermon-help/<locale>'` gives the help documents of a
 * locale (one chunk per language, loaded on demand).
 * @param {{ root: string }} options
 * @returns {import('vite').Plugin}
 */
export function helpPlugin({ root }) {
	return {
		name: 'sermon-help',
		resolveId(id) {
			if (id.startsWith(virtualPrefix) && locales.includes(id.slice(virtualPrefix.length)))
				return '\0' + id;
			return undefined;
		},
		load(id) {
			if (!id.startsWith('\0' + virtualPrefix)) return undefined;
			const { docs, files, problems } = loadHelp(root);
			for (const file of files) this.addWatchFile(file);
			// A production build fails on broken help texts; the dev server only warns while editing.
			for (const problem of problems) {
				if (this.meta.watchMode) this.warn(`help: ${problem}`);
				else this.error(`help: ${problem}`);
			}
			return `export default ${JSON.stringify(docs[id.slice(1 + virtualPrefix.length)] ?? {})};`;
		},
		handleHotUpdate({ file, server }) {
			if (!file.startsWith(root) || !file.endsWith('.md')) return undefined;
			for (const locale of locales) {
				const module = server.moduleGraph.getModuleById('\0' + virtualPrefix + locale);
				if (module) server.moduleGraph.invalidateModule(module);
			}
			server.ws.send({ type: 'full-reload' });
			return [];
		}
	};
}

if (argv[1] === fileURLToPath(import.meta.url)) {
	const [command, root = '../docs/help', src] = argv.slice(2);
	if (command !== 'lint' || !statSync(root, { throwIfNoEntry: false })?.isDirectory()) {
		console.error('usage: node scripts/help.js lint <helpDir> [srcDir]');
		exit(2);
	}
	const problems = lintHelp(root, src);
	if (problems.length) {
		for (const problem of problems) console.error(`✗ ${problem}`);
		exit(1);
	}
	console.log(`✓ help texts complete in ${locales.join(', ')}`);
}
