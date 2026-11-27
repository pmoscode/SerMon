import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import { fileURLToPath } from 'node:url';
import {
	helpPlugin,
	lintGuide,
	lintHelp,
	lintMessages,
	loadHelp,
	parseHelp,
	parseInline
} from './help.js';

const text = (t) => ({ type: 'text', text: t });

describe('parseInline', () => {
	it('parses emphasis, code and links', () => {
		expect(parseInline('Use **sudo** or *not*, see `top` and [load](help:metrics/load).')).toEqual([
			text('Use '),
			{ type: 'strong', content: [text('sudo')] },
			text(' or '),
			{ type: 'em', content: [text('not')] },
			text(', see '),
			{ type: 'code', text: 'top' },
			text(' and '),
			{ type: 'link', href: 'help:metrics/load', content: [text('load')] },
			text('.')
		]);
	});

	it('keeps markup characters as text', () => {
		expect(parseInline('<script>alert(1)</script> & snake_case_name')).toEqual([
			text('<script>alert(1)</script> & snake_case_name')
		]);
	});

	it.each(['javascript:alert(1)', 'http://example.org', '//evil.example', 'data:text/html,x'])(
		'rejects the link target %s',
		(href) => {
			const problems = [];
			// Only the text stays: the target never reaches the bundle.
			const parsed = parseInline(`[x](${href})`, problems);
			expect(parsed[0]).toEqual(text('x'));
			expect(parsed.some((node) => node.type === 'link')).toBe(false);
			expect(problems).toHaveLength(1);
		}
	);
});

describe('parseHelp', () => {
	it('splits title, summary and body', () => {
		const { doc, problems } = parseHelp(
			[
				'# CPU busy',
				'',
				'Share of time the CPU',
				'was not idle.',
				'',
				'## Causes',
				'',
				'- many processes',
				'- a busy',
				'  database',
				'',
				'1. check `top`',
				'',
				'```',
				'top -o %CPU',
				'```'
			].join('\n')
		);

		expect(problems).toEqual([]);
		expect(doc.title).toBe('CPU busy');
		expect(doc.summary).toEqual([text('Share of time the CPU was not idle.')]);
		expect(doc.body).toEqual([
			{ type: 'heading', level: 2, content: [text('Causes')] },
			{
				type: 'list',
				ordered: false,
				items: [[text('many processes')], [text('a busy database')]]
			},
			{ type: 'list', ordered: true, items: [[text('check '), { type: 'code', text: 'top' }]] },
			{ type: 'code', text: 'top -o %CPU' }
		]);
	});

	it.each([
		['no title', 'Summary only.', 'missing title'],
		['no summary', '# Title\n\n## Section', 'missing summary'],
		['deep heading', '# T\n\nS.\n\n#### Deep', 'heading level 4'],
		['table', '# T\n\nS.\n\n| a | b |', 'unsupported Markdown'],
		['open code block', '# T\n\nS.\n\n```\ncode', 'code block not closed']
	])('reports %s', (_name, markdown, problem) => {
		expect(parseHelp(markdown).problems.join('\n')).toContain(problem);
	});
});

describe('loadHelp and lintHelp', () => {
	let root;
	const write = (path, content) => {
		mkdirSync(dirname(join(root, path)), { recursive: true });
		writeFileSync(join(root, path), content);
	};

	afterEach(() => rmSync(root, { recursive: true, force: true }));

	function fixture() {
		root = mkdtempSync(join(tmpdir(), 'sermon-help-'));
		write('help/de/pages/fleet.md', '# Flotte\n\nAlle Server. Siehe [CPU](help:figures/cpu_busy).');
		write('help/en/pages/fleet.md', '# Fleet\n\nAll servers. See [CPU](help:figures/cpu_busy).');
		write('help/de/figures/cpu_busy.md', '# CPU\n\nAnteil.');
		write('help/en/figures/cpu_busy.md', '# CPU\n\nShare.');
		write('src/page.svelte', `<HelpTip topic="pages/fleet" />`);
	}

	it('loads the topics of every language', () => {
		fixture();
		const { docs, problems } = loadHelp(join(root, 'help'));

		expect(problems).toEqual([]);
		expect(Object.keys(docs.en).sort()).toEqual(['figures/cpu_busy', 'pages/fleet']);
		expect(docs.de['pages/fleet'].title).toBe('Flotte');
	});

	it('passes for complete texts', () => {
		fixture();
		expect(lintHelp(join(root, 'help'), join(root, 'src'))).toEqual([]);
	});

	it('fails when the English file is missing', () => {
		fixture();
		rmSync(join(root, 'help/en/figures/cpu_busy.md'));

		expect(lintHelp(join(root, 'help'))).toEqual([
			'en/figures/cpu_busy.md: missing (exists in another language)'
		]);
	});

	it('fails for links and uses of unknown topics', () => {
		fixture();
		write('help/en/pages/fleet.md', '# Fleet\n\nSee [load](help:figures/load).');
		write('src/other.ts', `const topic = 'figures/memory_used';`);

		expect(lintHelp(join(root, 'help'), join(root, 'src'))).toEqual([
			'en/pages/fleet.md: links to unknown topic "figures/load"',
			'other.ts: uses unknown help topic "figures/memory_used"'
		]);
	});

	// V1-10: a text nobody can open is a gap in the UI, not documentation.
	it('fails for topics shown nowhere', () => {
		fixture();
		write('help/de/modules/redis.md', '# Redis\n\nModul.');
		write('help/en/modules/redis.md', '# Redis\n\nModule. See [itself](help:modules/redis).');
		write('help/de/channels/slack.md', '# Slack\n\nKanal.');
		write('help/en/channels/slack.md', '# Slack\n\nChannel.');

		expect(lintHelp(join(root, 'help'), join(root, 'src'))).toEqual([
			'channels/slack: shown nowhere (not used by the UI nor linked from another topic)',
			'modules/redis: shown nowhere (not used by the UI nor linked from another topic)'
		]);
		// Without the source only the texts themselves are checked.
		expect(lintHelp(join(root, 'help'))).toEqual([]);
	});

	it('counts topics the UI builds from names of the server and links', () => {
		fixture();
		write('help/de/modules/redis.md', '# Redis\n\nModul.');
		write('help/en/modules/redis.md', '# Redis\n\nModule.');
		write('help/de/channels/slack.md', '# Slack\n\nKanal.');
		write('help/en/channels/slack.md', '# Slack\n\nChannel.');
		write('help/de/metrics/sermon_x.md', '# X\n\nX.');
		write('help/en/metrics/sermon_x.md', '# X\n\nX.');
		write('help/de/concepts/a.md', '# A\n\nSiehe [B](help:concepts/b).');
		write('help/en/concepts/a.md', '# A\n\nSee B.');
		write('help/de/concepts/b.md', '# B\n\nB.');
		write('help/en/concepts/b.md', '# B\n\nB.');
		write(
			'src/modules.svelte',
			'<HelpTip topic={`modules/${id}`} /><HelpTip topic={`channels/${type}`} />'
		);
		write('src/chart.svelte', `<HelpTip topic={'metrics/' + metric} /><x t="concepts/a" />`);

		expect(lintHelp(join(root, 'help'), join(root, 'src'))).toEqual([]);

		rmSync(join(root, 'src/modules.svelte'));
		write('src/modules.svelte', '<HelpTip topic={`modules/${id}`} />');
		expect(lintHelp(join(root, 'help'), join(root, 'src'))).toEqual([
			'channels/slack: shown nowhere (not used by the UI nor linked from another topic)'
		]);
	});
});

describe('lintGuide', () => {
	/** @type {string} */
	let root;
	const write = (path, content) => {
		mkdirSync(dirname(join(root, path)), { recursive: true });
		writeFileSync(join(root, path), content);
	};
	afterEach(() => rmSync(root, { recursive: true, force: true }));

	function fixture() {
		root = mkdtempSync(join(tmpdir(), 'sermon-guide-'));
		for (const [locale, title] of [
			['de', 'Flotte'],
			['en', 'Fleet']
		]) {
			write(`help/${locale}/pages/fleet.md`, `# ${title}\n\nText.`);
			write(`help/${locale}/concepts/tags.md`, `# Tags\n\nText.`);
		}
		write(
			'user/de/README.md',
			'# Handbuch\n\n1. [Flotte](#1-flotte-und-tags)\n\n## 1. Flotte und Tags\n\n' +
				'[Flotte](../../help/de/pages/fleet.md), [Tags](../../help/de/concepts/tags.md#tags), ' +
				'[EN](../en/README.md), [Web](https://example.org/x)'
		);
		write(
			'user/en/README.md',
			'# Guide\n\n## Fleet\n\n[Fleet](../../help/en/pages/fleet.md), [Tags](../../help/en/concepts/tags.md)'
		);
	}
	const lint = () => lintGuide(join(root, 'user'), join(root, 'help'));

	it('passes for a complete guide', () => {
		fixture();
		expect(lint()).toEqual([]);
	});

	it('fails for broken links and anchors and help of the other language', () => {
		fixture();
		write(
			'user/en/README.md',
			'# Guide\n\n[Contents](#fleet) [Fleet](../../help/de/pages/fleet.md) ' +
				'[Tags](../../help/en/concepts/tags.md) [Gone](../../help/en/pages/gone.md)'
		);
		expect(lint()).toEqual([
			'en/README.md: no heading for "#fleet"',
			'en/README.md: links a help text in "de"',
			'en/README.md: link "../../help/en/pages/gone.md" does not resolve',
			'en: the guide doesn\'t link "pages/fleet"'
		]);
	});

	it('fails when a language or a page of the UI is missing', () => {
		fixture();
		write('help/de/pages/logs.md', '# Logs\n\nText.');
		write('help/en/pages/logs.md', '# Logs\n\nText.');
		write('user/en/README.md', '# Guide\n\n[Fleet](../../help/en/pages/fleet.md)');
		expect(lint()).toEqual([
			'en: the guide doesn\'t link "concepts/tags"',
			'de: the guide doesn\'t link "pages/logs"',
			'en: the guide doesn\'t link "pages/logs"'
		]);
		rmSync(join(root, 'user/en'), { recursive: true });
		expect(lint()).toContain('en: no guide');
	});

	it('passes for the real guide', () => {
		const docs = fileURLToPath(new URL('../../docs', import.meta.url));
		expect(lintGuide(join(docs, 'user'), join(docs, 'help'))).toEqual([]);
	});
});

describe('lintMessages', () => {
	/** @type {string} */
	let dir;
	afterEach(() => rmSync(dir, { recursive: true, force: true }));

	const messages = (de, en) => {
		dir = mkdtempSync(join(tmpdir(), 'sermon-messages-'));
		writeFileSync(join(dir, 'de.json'), JSON.stringify(de));
		writeFileSync(join(dir, 'en.json'), JSON.stringify(en));
		return lintMessages(dir);
	};

	it('passes for complete texts', () => {
		expect(
			messages(
				{ $schema: 'x', save: 'Speichern', hello: 'Hallo {name}' },
				{ $schema: 'x', save: 'Save', hello: 'Hello {name}' }
			)
		).toEqual([]);
	});

	it('fails for missing, empty and differently parametrized texts', () => {
		expect(
			messages(
				{ save: 'Speichern', hello: 'Hallo {user}', help: ' ' },
				{ save: 'Save', hello: 'Hello {name}', help: 'Help', only: 'Only' }
			)
		).toEqual([
			'en.json: "hello" has other placeholders than de.json',
			'de.json: "help" missing',
			'de.json: "only" missing'
		]);
	});
});

// M5-29: every page loads only the titles, popovers and the panel the full texts – in a build as a
// JSON file, data rather than JavaScript (check:size).
describe('helpPlugin', () => {
	const plugin = helpPlugin({ root: fileURLToPath(new URL('../../docs/help', import.meta.url)) });
	const context = (build) => {
		plugin.configResolved({ command: build ? 'build' : 'serve' });
		const emitted = [];
		return {
			emitted,
			addWatchFile() {},
			warn() {},
			error(message) {
				throw new Error(message);
			},
			emitFile(file) {
				emitted.push(file);
				return `ref${emitted.length}`;
			},
			meta: { watchMode: false }
		};
	};
	const code = (id, ctx = context(true)) => plugin.load.call(ctx, plugin.resolveId(id));
	const literal = (source, prefix) => JSON.parse(source.slice(prefix.length).replace(/\)?;$/, ''));

	it('serves titles as code and the full texts of a build as a JSON file it fetches', () => {
		const titles = literal(code('virtual:sermon-help/en/titles'), 'export default ');
		const build = context(true);
		const fetched = code('virtual:sermon-help/en', build);
		expect(build.emitted).toHaveLength(1);
		expect(build.emitted[0]).toMatchObject({ type: 'asset', name: 'help-en.json' });
		expect(fetched).toContain('fetch(import.meta.ROLLUP_FILE_URL_ref1)');
		expect(fetched).toContain('if (!r.ok) throw');
		expect(fetched).not.toContain('pages/fleet');
		const full = JSON.parse(build.emitted[0].source);
		expect(titles['pages/fleet']).toEqual({ title: 'Fleet', summary: [], body: [], more: true });
		expect(full['pages/fleet'].summary.length).toBeGreaterThan(0);
		expect(full['pages/fleet'].body.length).toBeGreaterThan(0);
		expect(Object.keys(titles)).toEqual(Object.keys(full));
		expect(JSON.stringify(titles).length).toBeLessThan(JSON.stringify(full).length / 5);
		// The German texts are their own file.
		const de = context(true);
		code('virtual:sermon-help/de', de);
		expect(de.emitted[0].name).toBe('help-de.json');
		expect(JSON.parse(de.emitted[0].source)['pages/fleet'].title).toBe('Flotte');
	});

	it('serves the full texts inline to the dev server and the tests', () => {
		const dev = context(false);
		const inline = code('virtual:sermon-help/en', dev);
		expect(dev.emitted).toHaveLength(0);
		const full = literal(inline, 'export default Promise.resolve(');
		expect(full['pages/fleet'].body.length).toBeGreaterThan(0);
	});

	it('knows no other parts', () => {
		expect(plugin.resolveId('virtual:sermon-help/en/summary')).toBeUndefined();
		expect(plugin.resolveId('virtual:sermon-help/en/other')).toBeUndefined();
		expect(plugin.resolveId('virtual:sermon-help/xx/titles')).toBeUndefined();
	});
});
