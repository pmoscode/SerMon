import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { TimeRangeState } from './timerange.svelte.js';

const now = Date.UTC(2026, 10, 19, 12, 0, 0);

describe('TimeRangeState', () => {
	beforeEach(() => {
		vi.useFakeTimers();
		vi.setSystemTime(now);
	});
	afterEach(() => vi.useRealTimers());

	it('shows the last hour by default and follows the time', () => {
		const state = new TimeRangeState();
		expect(state.range).toEqual({ from: now - 3600_000, to: now });
		expect(state.live).toBe(true);

		vi.setSystemTime(now + 60_000);
		state.refresh();

		expect(state.range).toEqual({ from: now - 3540_000, to: now + 60_000 });
	});

	it('zooms every reader to the selection and returns to the preset', () => {
		const state = new TimeRangeState();
		state.setPreset('24h');
		const readers = [() => state.range, () => state.range];

		state.zoom(now - 7200_000, now - 3600_000);

		for (const read of readers)
			expect(read()).toEqual({ from: now - 7200_000, to: now - 3600_000 });
		expect(state.live).toBe(false);
		vi.setSystemTime(now + 60_000);
		state.refresh();
		expect(state.range.to).toBe(now - 3600_000);

		state.reset();
		expect(state.range).toEqual({ from: now + 60_000 - 86400_000, to: now + 60_000 });
	});

	it('ignores clicks and inverted selections', () => {
		const state = new TimeRangeState();
		state.zoom(now - 5000, now);
		state.zoom(now, now - 3600_000);
		state.zoom(Number.NaN, now);

		expect(state.live).toBe(true);
	});

	it('leaves the zoom when a preset is chosen', () => {
		const state = new TimeRangeState();
		state.zoom(now - 7200_000, now - 3600_000);

		state.setPreset('15m');

		expect(state.range).toEqual({ from: now - 900_000, to: now });
	});
});
