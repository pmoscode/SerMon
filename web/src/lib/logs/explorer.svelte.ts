import { BACKGROUND } from '#lib/processes/live.svelte.js';
import { create } from '@bufbuild/protobuf';
import {
	timestampFromMs,
	timestampMs,
	TimestampSchema,
	type Timestamp
} from '@bufbuild/protobuf/wkt';
import { Code, codeOf } from '#lib/api/client.js';
import type { GetLogHistogramResponse, LogLine } from '#lib/api/gen/sermon/web/v1/log_pb.js';
import { LogSource } from '#lib/api/gen/sermon/web/v1/log_pb.js';
import { LogSeverity } from '#lib/api/gen/sermon/web/v1/log_settings_pb.js';
import { logsApi } from '#lib/api/logs.js';
import { m } from '#lib/paraglide/messages.js';
import { rangeOf, severities, type LogFilter } from './filter.js';
import { lineMs, MAX_LINES, merge } from './lines.js';
import type { CountSeries } from './histogram.js';

/** Lines of one page of a query. */
export const PAGE = 200;
/** A tail starts at the newest shown line if it is this recent (the server allows 5 minutes). */
const TAIL_SINCE = 4.5 * 60_000;
/** Waits between reconnects of a failed tail. */
const RETRY_FIRST = 1000;
const RETRY_MAX = 30_000;
/** A tail that ends normally sooner than this counts as failed for the wait before the next. */
const SHORT_STREAM = 10_000;

/** The selection of a filter as the API takes it; a fixed node replaces the node filter. */
export function selection(f: LogFilter, fixedNode?: string) {
	return {
		nodeIds: fixedNode ? [fixedNode] : f.nodes,
		groupId: fixedNode ? undefined : f.group,
		sources: f.sources.map((s) =>
			s === 'file' ? LogSource.FILE : s === 'container' ? LogSource.CONTAINER : LogSource.JOURNAL
		),
		units: f.units,
		minSeverity: f.severity
			? ((severities.indexOf(f.severity) + 1) as LogSeverity)
			: LogSeverity.UNSPECIFIED,
		text: f.text,
		ignoreCase: f.ignoreCase
	};
}

/** The end of a range just after a line (ends are exclusive). */
function justAfter(at: Timestamp): Timestamp {
	return at.nanos >= 999_999_999
		? create(TimestampSchema, { seconds: at.seconds + 1n, nanos: 0 })
		: create(TimestampSchema, { seconds: at.seconds, nanos: at.nanos + 1 });
}

function sleep(ms: number, signal: AbortSignal): Promise<void> {
	return new Promise((done) => {
		const timer = setTimeout(done, ms);
		signal.addEventListener('abort', () => {
			clearTimeout(timer);
			done();
		});
	});
}

/** The message for a failed query or tail. */
function failure(error: unknown): string {
	switch (codeOf(error)) {
		case Code.Unavailable:
			return m.logs_error_unavailable();
		case Code.PermissionDenied:
			return m.logs_error_forbidden();
		case Code.InvalidArgument:
			return m.logs_error_invalid();
		case Code.FailedPrecondition:
			return m.logs_error_too_broad();
		case Code.ResourceExhausted:
			return m.logs_error_too_many_live();
		default:
			return m.error_unavailable();
	}
}

export interface HistogramData {
	step: number;
	range: { from: number; to: number };
	series: CountSeries[];
}

function histogramData(res: GetLogHistogramResponse, range: { from: number; to: number }) {
	const step = Number(res.step?.seconds ?? 0n) * 1000 + (res.step?.nanos ?? 0) / 1e6;
	const series = res.series.map((s) => ({
		severity: s.severity,
		points: s.points.map((p) => ({ at: p.at ? timestampMs(p.at) : 0, count: p.count }))
	}));
	return { step, range, series };
}

export type LiveState = 'off' | 'connecting' | 'live' | 'reconnecting';

/**
 * The state of a log explorer: the lines of a query with further pages, the histogram, and the
 * live tail that adds new lines (paused: they wait in a buffer).
 */
export class LogExplorerState {
	lines = $state.raw<LogLine[]>([]);
	status = $state<'loading' | 'ready' | 'error'>('loading');
	error = $state('');
	/** The range holds more lines than shown. */
	more = $state(false);
	loadingMore = $state(false);
	histogram = $state.raw<HistogramData | undefined>(undefined);
	histogramFailed = $state(false);
	/** Loki gave up counting a text or units over the range (V1-24): shorten it. */
	histogramTooBroad = $state(false);

	live = $state<LiveState>('off');
	paused = $state(false);
	/** Lines that arrived while paused. */
	pending = $state(0);
	/** The last message of the tail skipped lines (more than 100/s). */
	throttled = $state(false);
	/** The tail can't reach the log store right now. */
	storeDown = $state(false);
	liveError = $state('');

	#filter: LogFilter | undefined;
	#fixedNode: string | undefined;
	#range = { from: 0, to: 0 };
	#search: AbortController | undefined;
	#tail: AbortController | undefined;
	#buffer: LogLine[] = [];

	/** Runs the query and the histogram of the filter; a running tail follows the new filter. */
	async search(filter: LogFilter, fixedNode?: string, now = Date.now()): Promise<void> {
		this.#search?.abort();
		const ctrl = new AbortController();
		this.#search = ctrl;
		this.#filter = filter;
		this.#fixedNode = fixedNode;
		this.#range = rangeOf(filter, now);
		const range = this.#range;
		const request = {
			...selection(filter, fixedNode),
			from: timestampFromMs(range.from),
			to: timestampFromMs(range.to)
		};
		this.status = 'loading';
		this.error = '';
		this.histogramFailed = false;
		this.histogramTooBroad = false;
		if (this.live !== 'off') this.#restartTail();
		const lines = logsApi.logs
			.queryLogs({ filter: request, limit: PAGE }, { signal: ctrl.signal })
			.then((res) => {
				if (ctrl.signal.aborted) return;
				this.lines = merge([], res.lines);
				this.more = res.more;
				this.status = 'ready';
			})
			.catch((error: unknown) => {
				if (ctrl.signal.aborted || codeOf(error) === Code.Unauthenticated) return;
				this.lines = [];
				this.more = false;
				this.error = failure(error);
				this.status = 'error';
			});
		const histogram = logsApi.logs
			.getLogHistogram({ filter: request }, { signal: ctrl.signal })
			.then((res) => {
				if (!ctrl.signal.aborted) this.histogram = histogramData(res, range);
			})
			.catch((error: unknown) => {
				if (ctrl.signal.aborted) return;
				this.histogram = undefined;
				if (codeOf(error) === Code.FailedPrecondition) this.histogramTooBroad = true;
				else this.histogramFailed = true;
			});
		await Promise.all([lines, histogram]);
	}

	/** Loads the next older page of the range. */
	async loadMore(): Promise<void> {
		const oldest = this.lines.at(-1);
		if (!this.#filter || !oldest?.at || this.loadingMore) return;
		this.loadingMore = true;
		try {
			const res = await logsApi.logs.queryLogs({
				filter: {
					...selection(this.#filter, this.#fixedNode),
					from: timestampFromMs(this.#range.from),
					to: justAfter(oldest.at)
				},
				limit: PAGE
			});
			const before = this.lines.length;
			this.lines = merge(this.lines, res.lines);
			this.more = res.more && this.lines.length > before && this.lines.length < MAX_LINES;
		} catch (error) {
			if (codeOf(error) !== Code.Unauthenticated) this.error = failure(error);
		} finally {
			this.loadingMore = false;
		}
	}

	/** The lines before and after a line in its stream, oldest first. */
	async context(line: LogLine): Promise<LogLine[]> {
		const res = await logsApi.logs.getLogContext({
			nodeId: line.nodeId,
			at: line.at,
			source: line.source,
			unit: line.unit
		});
		return res.lines;
	}

	/** Starts the live tail with the current filter. */
	startTail(): void {
		if (this.live !== 'off' || !this.#filter) return;
		this.liveError = '';
		void this.#runTail(this.#filter, this.#fixedNode);
	}

	/** Ends the tail; lines waiting from a pause are shown. */
	stopTail(): void {
		this.#tail?.abort();
		this.#tail = undefined;
		this.live = 'off';
		this.resume();
		this.throttled = false;
		this.storeDown = false;
	}

	/** Keeps the list still; new lines wait until resume. */
	pause(): void {
		if (this.live !== 'off') this.paused = true;
	}

	resume(): void {
		this.paused = false;
		if (this.#buffer.length > 0) this.lines = merge(this.lines, this.#buffer);
		this.#buffer = [];
		this.pending = 0;
	}

	/** Ends everything (the view goes). */
	destroy(): void {
		this.#search?.abort();
		this.stopTail();
	}

	#restartTail(): void {
		this.#tail?.abort();
		this.live = 'off';
		this.startTail();
	}

	#receive(lines: LogLine[]): void {
		if (lines.length === 0) return;
		if (this.paused) {
			this.#buffer = merge(this.#buffer, lines);
			this.pending = this.#buffer.length;
		} else {
			this.lines = merge(this.lines, lines);
		}
	}

	/** Where a (re)connected tail starts: at the newest line, if it is recent enough. */
	#since(): Timestamp | undefined {
		const newest = this.#buffer[0] ?? this.lines[0];
		return newest?.at && lineMs(newest) > Date.now() - TAIL_SINCE ? newest.at : undefined;
	}

	async #runTail(filter: LogFilter, fixedNode: string | undefined): Promise<void> {
		const ctrl = new AbortController();
		this.#tail = ctrl;
		this.live = 'connecting';
		let wait = RETRY_FIRST;
		let reopen = false;
		while (!ctrl.signal.aborted) {
			const opened = Date.now();
			// Reopening by itself is no activity of the user (M3-28).
			const headers = reopen ? BACKGROUND : undefined;
			reopen = true;
			try {
				const request = { ...selection(filter, fixedNode), since: this.#since() };
				for await (const res of logsApi.logs.tailLogs(request, { signal: ctrl.signal, headers })) {
					this.live = 'live';
					this.throttled = res.throttled;
					this.storeDown = res.unavailable;
					this.#receive(res.lines);
				}
				// A normal end (15 minutes, changed rights): open the next one at once – unless the
				// stream ended right away, then wait as after a failure (no loop of requests).
				if (Date.now() - opened >= SHORT_STREAM) {
					wait = RETRY_FIRST;
					continue;
				}
				await sleep(wait, ctrl.signal);
				wait = Math.min(wait * 2, RETRY_MAX);
			} catch (error) {
				if (ctrl.signal.aborted) break;
				const code = codeOf(error);
				if (code === Code.Unauthenticated) break;
				if (
					code === Code.PermissionDenied ||
					code === Code.ResourceExhausted ||
					code === Code.FailedPrecondition ||
					code === Code.InvalidArgument
				) {
					this.liveError = failure(error);
					break;
				}
				this.live = 'reconnecting';
				await sleep(wait, ctrl.signal);
				wait = Math.min(wait * 2, RETRY_MAX);
			}
		}
		if (this.#tail === ctrl) {
			this.#tail = undefined;
			this.live = 'off';
			this.resume();
		}
	}
}
