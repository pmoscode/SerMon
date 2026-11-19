/**
 * The global time range of a page (KONZEPT §17): a preset relative to now ("last hour") or an
 * absolute range from zooming into a chart. Every chart of the page reads it, so zooming one
 * zooms all of them.
 */

export const presets = ['15m', '1h', '6h', '24h', '7d', '30d'] as const;
export type Preset = (typeof presets)[number];

const presetSeconds: Record<Preset, number> = {
	'15m': 15 * 60,
	'1h': 3600,
	'6h': 6 * 3600,
	'24h': 24 * 3600,
	'7d': 7 * 86400,
	'30d': 30 * 86400
};

/** A range in milliseconds since the epoch, from inclusive, to exclusive. */
export interface Range {
	from: number;
	to: number;
}

/** Zooming below this span (ms) is ignored: a click, not a selection. */
const MIN_SPAN = 10_000;

export class TimeRangeState {
	/** The chosen preset; still set while zoomed, to return to it. */
	preset = $state<Preset>('1h');
	/** The zoomed range, if any. */
	zoomed = $state<Range | undefined>(undefined);
	/** "Now" of the relative presets; refresh() moves it. */
	now = $state(Date.now());

	/** The range all charts show. */
	readonly range: Range = $derived(
		this.zoomed ?? { from: this.now - presetSeconds[this.preset] * 1000, to: this.now }
	);

	/** True while the range follows the current time (no zoom). */
	get live(): boolean {
		return this.zoomed === undefined;
	}

	setPreset(preset: Preset): void {
		this.preset = preset;
		this.zoomed = undefined;
		this.now = Date.now();
	}

	/** Zooms all charts to the range (ignored for tiny or inverted ranges). */
	zoom(from: number, to: number): void {
		if (!Number.isFinite(from) || !Number.isFinite(to) || to - from < MIN_SPAN) return;
		this.zoomed = { from, to };
	}

	/** Back to the preset. */
	reset(): void {
		this.zoomed = undefined;
		this.now = Date.now();
	}

	/** Moves a relative range to the current time (live mode); a zoomed range stays. */
	refresh(): void {
		if (this.live) this.now = Date.now();
	}
}

export const timeRange = new TimeRangeState();
