/**
 * Log lines come from the monitored hosts and are shown as text only (KONZEPT §10.4): ANSI escape
 * sequences are removed, control and bidi characters are replaced by visible symbols, so a line
 * can neither style the terminal-like view nor reorder what the reader sees ("Trojan source").
 */

/** A piece of a line; marker is a visible replacement of an invisible character. */
export interface TextPart {
	text: string;
	marker: boolean;
}

// CSI (also the 8-bit form) and the other two-byte escapes: they carry no text and only style or
// move the terminal-like view, so they go silently.
const ANSI =
	// eslint-disable-next-line no-control-regex
	/\x1b\[[0-?]*[ -/]*[@-~]|\x9b[0-?]*[ -/]*[@-~]|\x1b[@-Z\\-_]/g;

// OSC (titles, hyperlinks) up to BEL or ST carries text: shown as a marker with its content, so
// nothing between two fields of a line can hide (M3-28).
// eslint-disable-next-line no-control-regex
const OSC = /\x1b\]([^\x07\x1b]*)(?:\x07|\x1b\\)/g;

// Control, format (bidi, zero-width, tags, soft hyphen) and separator characters, and the
// Hangul fillers that render blank – as the server's textsafe (M3-28). Tab and line feed stay.
const HIDDEN = /[\p{Cc}\p{Cf}\p{Zl}\p{Zp}\u115f\u1160\u3164\uffa0]/u;
const PICTOGRAPHIC = /\p{Extended_Pictographic}/u;
const ZWJ = '\u200d';

function visible(char: string): string {
	const code = char.codePointAt(0)!;
	if (code < 0x20) return String.fromCodePoint(0x2400 + code); // ␀ … ␟
	if (code === 0x7f) return '␡';
	return `⟨U+${code.toString(16).toUpperCase().padStart(4, '0')}⟩`;
}

function hidden(chars: string[], i: number): boolean {
	const char = chars[i]!;
	if (char === '\t' || char === '\n' || !HIDDEN.test(char)) return false;
	// The joiner inside an emoji sequence (👩‍💻) is part of the picture.
	return !(
		char === ZWJ &&
		PICTOGRAPHIC.test(chars[i - 1] ?? '') &&
		PICTOGRAPHIC.test(chars[i + 1] ?? '')
	);
}

function plainParts(text: string, parts: TextPart[]): void {
	let run = '';
	const chars = Array.from(text.replace(ANSI, ''));
	for (let i = 0; i < chars.length; i++) {
		if (hidden(chars, i)) {
			if (run) parts.push({ text: run, marker: false });
			parts.push({ text: visible(chars[i]!), marker: true });
			run = '';
		} else {
			run += chars[i];
		}
	}
	if (run) parts.push({ text: run, marker: false });
}

/** The line without escape sequences, split into text and visible replacements. */
export function displayParts(line: string): TextPart[] {
	const parts: TextPart[] = [];
	let last = 0;
	for (const match of line.matchAll(OSC)) {
		plainParts(line.slice(last, match.index), parts);
		const inner: TextPart[] = [];
		plainParts(match[1] ?? '', inner);
		parts.push({ text: `⟨OSC ${inner.map((p) => p.text).join('')}⟩`, marker: true });
		last = match.index + match[0].length;
	}
	plainParts(line.slice(last), parts);
	return parts;
}

/** The line as it is shown. */
export function displayText(line: string): string {
	return displayParts(line)
		.map((p) => p.text)
		.join('');
}
