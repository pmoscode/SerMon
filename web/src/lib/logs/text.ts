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

// CSI (also the 8-bit form), OSC up to BEL or ST, and the other two-byte escapes.
const ANSI =
	// eslint-disable-next-line no-control-regex
	/\x1b\[[0-?]*[ -/]*[@-~]|\x9b[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[@-Z\\-_]/g;

// C0 without tab and line feed, DEL, C1, bidi controls, invisible marks and separators.
const HIDDEN =
	// eslint-disable-next-line no-control-regex
	/[\x00-\x08\x0b-\x1f\x7f\x80-\x9f\u061c\u200b\u200e\u200f\u2028\u2029\u202a-\u202e\u2066-\u2069\ufeff]/;

function visible(char: string): string {
	const code = char.codePointAt(0)!;
	if (code < 0x20) return String.fromCodePoint(0x2400 + code); // ␀ … ␟
	if (code === 0x7f) return '␡';
	return `⟨U+${code.toString(16).toUpperCase().padStart(4, '0')}⟩`;
}

/** The line without escape sequences, split into text and visible replacements. */
export function displayParts(line: string): TextPart[] {
	const parts: TextPart[] = [];
	let text = '';
	for (const char of line.replace(ANSI, '')) {
		if (HIDDEN.test(char)) {
			if (text) parts.push({ text, marker: false });
			parts.push({ text: visible(char), marker: true });
			text = '';
		} else {
			text += char;
		}
	}
	if (text) parts.push({ text, marker: false });
	return parts;
}

/** The line as it is shown. */
export function displayText(line: string): string {
	return displayParts(line)
		.map((p) => p.text)
		.join('');
}
