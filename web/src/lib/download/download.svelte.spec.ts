import { afterEach, describe, expect, it, vi } from 'vitest';
import { REVOKE_AFTER_MS, saveFile } from './download.js';

describe('saveFile', () => {
	afterEach(() => {
		vi.restoreAllMocks();
		vi.useRealTimers();
	});

	// Revoked right after the click, some browsers cancel the download (M2-39): the URL stays
	// valid a while.
	it('downloads the data and frees the URL only later', () => {
		vi.useFakeTimers();
		const create = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:x');
		const revoke = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {});
		const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
		saveFile('a: 1\n', 'rules.yaml', 'text/yaml');
		expect(create).toHaveBeenCalledOnce();
		expect(click).toHaveBeenCalledOnce();
		expect(revoke).not.toHaveBeenCalled();
		vi.advanceTimersByTime(REVOKE_AFTER_MS);
		expect(revoke).toHaveBeenCalledWith('blob:x');
	});
});
