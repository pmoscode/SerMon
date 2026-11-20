import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import { lintHelp, loadHelp, parseHelp, parseInline } from './help.js';

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
});
