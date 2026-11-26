import { describe, expect, it } from 'vitest';
import { DEFAULT_ROTATE, kioskUrl, MAX_BOARDS, parseFragment } from './fragment.js';

const tok = (c: string) => `smk_${c.repeat(43)}`;

describe('kiosk fragment', () => {
	it('reads the tokens and the rotation', () => {
		expect(parseFragment(`#t=${tok('a')},${tok('b')}&rotate=30`)).toEqual({
			tokens: [tok('a'), tok('b')],
			rotate: 30
		});
		expect(parseFragment(`#t=${tok('a')}`)).toEqual({ tokens: [tok('a')], rotate: DEFAULT_ROTATE });
	});

	it('drops what is no token, duplicates and more than ten', () => {
		const many = Array.from({ length: 12 }, (_, i) => tok(String.fromCharCode(97 + i))).join(',');
		expect(parseFragment(`#t=${many}`).tokens).toHaveLength(MAX_BOARDS);
		expect(parseFragment(`#t=${tok('a')},${tok('a')},smk_short,<script>`).tokens).toEqual([
			tok('a')
		]);
		expect(parseFragment('').tokens).toEqual([]);
	});

	it('bounds the rotation', () => {
		expect(parseFragment('#rotate=1').rotate).toBe(DEFAULT_ROTATE);
		expect(parseFragment('#rotate=x').rotate).toBe(DEFAULT_ROTATE);
		expect(parseFragment('#rotate=99999').rotate).toBe(3600);
	});

	it('builds the address of a display', () => {
		expect(kioskUrl('https://sermon.example', [tok('a'), tok('b')])).toBe(
			`https://sermon.example/kiosk#t=${tok('a')},${tok('b')}`
		);
		expect(parseFragment(new URL(kioskUrl('https://x.example', [tok('c')], 20)).hash)).toEqual({
			tokens: [tok('c')],
			rotate: 20
		});
	});
});
