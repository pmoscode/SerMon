import { Code, codeOf } from '#lib/api/client.js';
import type { ProcessSnapshot } from '#lib/api/gen/sermon/web/v1/live_view_pb.js';
import { liveViewApi } from '#lib/api/liveview.js';

export type ViewStatus =
	'stopped' | 'connecting' | 'live' | 'offline' | 'reconnecting' | 'refused' | 'failed';

/** Why the view gave up: no right, a node the user doesn't see, too many live streams. */
export type ViewFailure = 'forbidden' | 'not_found' | 'too_many';

export interface ViewTiming {
	/** Waits between reconnects of a failed stream. */
	retryFirst: number;
	retryMax: number;
	/** A stream that ends normally sooner than this counts as failed for the wait. */
	shortStream: number;
}

const TIMING: ViewTiming = { retryFirst: 1000, retryMax: 30_000, shortStream: 10_000 };

function sleep(ms: number, signal: AbortSignal): Promise<void> {
	return new Promise((done) => {
		const timer = setTimeout(done, ms);
		signal.addEventListener('abort', () => {
			clearTimeout(timer);
			done();
		});
	});
}

function failureOf(code: Code): ViewFailure | undefined {
	switch (code) {
		case Code.PermissionDenied:
			return 'forbidden';
		case Code.NotFound:
		case Code.InvalidArgument:
			return 'not_found';
		case Code.ResourceExhausted:
			return 'too_many';
		default:
			return undefined;
	}
}

/**
 * The live process view of a node (KONZEPT §9, M3-15): the newest snapshot, the state of the
 * stream and the pause. The stream reconnects after its normal end (15 minutes, changed rights or
 * switch) and after outages; a refusal of the agent and missing rights end it.
 */
export class ProcessViewState {
	status = $state<ViewStatus>('stopped');
	/** The snapshot shown; while paused the one of the moment of the pause. */
	snapshot = $state.raw<ProcessSnapshot | undefined>(undefined);
	paused = $state(false);
	/** A newer snapshot waits until the pause ends. */
	waiting = $state(false);
	/** The agent's reason for refusing (agent.toml), a text of the host. */
	reason = $state('');
	failure = $state<ViewFailure | undefined>(undefined);

	#timing: ViewTiming;
	#ctrl: AbortController | undefined;
	#latest: ProcessSnapshot | undefined;

	constructor(timing: Partial<ViewTiming> = {}) {
		this.#timing = { ...TIMING, ...timing };
	}

	/** Opens the stream of the node; a running one stays. */
	start(nodeId: string): void {
		if (this.#ctrl) return;
		this.reason = '';
		this.failure = undefined;
		const ctrl = new AbortController();
		this.#ctrl = ctrl;
		void this.#run(nodeId, ctrl);
	}

	/** Closes the stream (the tab goes); the server stops the agent 30 s later. */
	stop(): void {
		this.#ctrl?.abort();
		this.#ctrl = undefined;
		this.status = 'stopped';
	}

	/** Keeps the shown snapshot; the stream keeps running. */
	pause(): void {
		this.paused = true;
	}

	resume(): void {
		this.paused = false;
		if (this.#latest) this.snapshot = this.#latest;
		this.waiting = false;
	}

	#receive(snapshot: ProcessSnapshot): void {
		this.#latest = snapshot;
		if (this.paused) this.waiting = true;
		else this.snapshot = snapshot;
	}

	async #run(nodeId: string, ctrl: AbortController): Promise<void> {
		const { retryFirst, retryMax, shortStream } = this.#timing;
		this.status = 'connecting';
		let wait = retryFirst;
		let end: ViewStatus = 'stopped';
		while (!ctrl.signal.aborted) {
			const opened = Date.now();
			try {
				let refused = false;
				for await (const res of liveViewApi.liveView.watchProcesses(
					{ nodeId },
					{ signal: ctrl.signal }
				)) {
					const event = res.event;
					if (event.case === 'snapshot') {
						this.status = 'live';
						this.#receive(event.value);
					} else if (event.case === 'offline') {
						this.status = 'offline';
					} else if (event.case === 'refused') {
						this.reason = event.value;
						refused = true;
						break;
					}
				}
				if (refused) {
					end = 'refused';
					break;
				}
				// A normal end: open the next one at once – unless it ended right away (no loop).
				if (Date.now() - opened >= shortStream) {
					wait = retryFirst;
					continue;
				}
				await sleep(wait, ctrl.signal);
				wait = Math.min(wait * 2, retryMax);
			} catch (error) {
				if (ctrl.signal.aborted) break;
				const code = codeOf(error);
				if (code === Code.Unauthenticated) break;
				const failure = failureOf(code);
				if (failure) {
					this.failure = failure;
					end = 'failed';
					break;
				}
				this.status = 'reconnecting';
				await sleep(wait, ctrl.signal);
				wait = Math.min(wait * 2, retryMax);
			}
		}
		if (this.#ctrl === ctrl) {
			this.#ctrl = undefined;
			this.status = end;
		}
	}
}
