import { describe, expect, it } from 'vitest';
import { displayParts, displayText } from './text.js';

describe('displayText', () => {
	it('removes ANSI colour and cursor sequences', () => {
		expect(displayText('\x1b[1;31mERROR\x1b[0m done')).toBe('ERROR done');
		expect(displayText('\x1b[2K\x1b[1Gprogress 50%')).toBe('progress 50%');
		expect(displayText('a\x9b31mb')).toBe('ab');
	});

	it('removes OSC sequences such as terminal titles and links', () => {
		expect(displayText('\x1b]0;evil title\x07rest')).toBe('rest');
		expect(displayText('\x1b]8;;https://evil.example\x1b\\click\x1b]8;;\x1b\\')).toBe('click');
	});

	it('shows control characters as visible symbols', () => {
		expect(displayText('bell\x07 nul\x00 cr\r esc\x1b del\x7f')).toBe('bell␇ nul␀ cr␍ esc␛ del␡');
		expect(displayText('c1\x85')).toBe('c1⟨U+0085⟩');
	});

	it('keeps tabs and line breaks of multi-line entries', () => {
		expect(displayText('Traceback:\n\tat main()')).toBe('Traceback:\n\tat main()');
	});

	it('shows bidi and invisible characters instead of applying them', () => {
		// "Trojan source": the override would show the text reversed.
		expect(displayText('user\u202Enimda\u202C')).toBe('user⟨U+202E⟩nimda⟨U+202C⟩');
		expect(displayText('\u2066x\u2069\u200Fy\u061C\uFEFF\u200B')).toBe(
			'⟨U+2066⟩x⟨U+2069⟩⟨U+200F⟩y⟨U+061C⟩⟨U+FEFF⟩⟨U+200B⟩'
		);
	});

	it('leaves ordinary text, markup and emoji alone', () => {
		const text = '<script>alert(1)</script> & "quotes" 👩‍💻 Grüße ü';
		expect(displayText(text)).toBe(text);
	});
});

describe('displayParts', () => {
	it('marks the replaced characters', () => {
		expect(displayParts('a\x07b\u202Ec')).toEqual([
			{ text: 'a', marker: false },
			{ text: '␇', marker: true },
			{ text: 'b', marker: false },
			{ text: '⟨U+202E⟩', marker: true },
			{ text: 'c', marker: false }
		]);
		expect(displayParts('')).toEqual([]);
	});
});
