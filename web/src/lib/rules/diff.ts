/** A line of a diff: kept, removed (before only) or added (after only). */
export interface DiffLine {
	kind: 'same' | 'del' | 'add';
	text: string;
}

function lines(text: string): string[] {
	if (text === '') return [];
	return text.replace(/\n$/, '').split('\n');
}

/**
 * The line diff of two texts (longest common subsequence, after trimming the common start and
 * end so that small edits of long rule documents stay cheap).
 */
export function diffLines(before: string, after: string): DiffLine[] {
	const a = lines(before);
	const b = lines(after);
	let head = 0;
	while (head < a.length && head < b.length && a[head] === b[head]) head++;
	let tail = 0;
	while (
		tail < a.length - head &&
		tail < b.length - head &&
		a[a.length - 1 - tail] === b[b.length - 1 - tail]
	)
		tail++;
	const x = a.slice(head, a.length - tail);
	const y = b.slice(head, b.length - tail);
	// LCS table over the middle part.
	const n = x.length;
	const m = y.length;
	const lcs: number[][] = Array.from({ length: n + 1 }, () => new Array<number>(m + 1).fill(0));
	for (let i = n - 1; i >= 0; i--) {
		for (let j = m - 1; j >= 0; j--) {
			lcs[i][j] = x[i] === y[j] ? lcs[i + 1][j + 1] + 1 : Math.max(lcs[i + 1][j], lcs[i][j + 1]);
		}
	}
	const middle: DiffLine[] = [];
	let i = 0;
	let j = 0;
	while (i < n && j < m) {
		if (x[i] === y[j]) {
			middle.push({ kind: 'same', text: x[i] });
			i++;
			j++;
		} else if (lcs[i + 1][j] >= lcs[i][j + 1]) {
			middle.push({ kind: 'del', text: x[i++] });
		} else {
			middle.push({ kind: 'add', text: y[j++] });
		}
	}
	while (i < n) middle.push({ kind: 'del', text: x[i++] });
	while (j < m) middle.push({ kind: 'add', text: y[j++] });
	return [
		...a.slice(0, head).map((text) => ({ kind: 'same' as const, text })),
		...middle,
		...a.slice(a.length - tail).map((text) => ({ kind: 'same' as const, text }))
	];
}
