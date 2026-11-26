/**
 * The state of a kiosk display (M5-27): one board per token, refreshed every 30 s, rotating among
 * the boards it can show. A board whose token stops working (revoked, expired, dashboard private)
 * shows nothing any more; one that can't reach the server keeps its last data and says since
 * when it is stale, until a refresh succeeds again.
 */
import { Code, ConnectError } from '@connectrpc/connect';
import type { DashboardSpec } from '#lib/api/gen/sermon/web/v1/dashboard_pb.js';
import type { WidgetSource } from '#lib/dashboards/source.js';
import type { KioskClient } from './api.js';
import { kioskSource } from './source.js';

export type BoardStatus = 'loading' | 'ready' | 'invalid' | 'offline';

export interface Board {
	token: string;
	status: BoardStatus;
	spec?: DashboardSpec;
	revision: bigint;
	/** When the data stopped being current (a failed refresh of the dashboard or a widget). */
	staleSince?: number;
	/** Widgets whose last load failed. */
	failing: string[];
	source: WidgetSource;
}

export const REFRESH_MS = 30_000;

export class KioskState {
	// Raw: boards are replaced, never changed in place; a deep proxy would make every spec new.
	boards = $state.raw<Board[]>([]);
	index = $state(0);
	/** Not reactive: the clients never change. */
	#clients: Record<string, KioskClient> = {};
	#now: () => number;

	constructor(
		tokens: string[],
		deps: { client: (token: string) => KioskClient; now?: () => number }
	) {
		this.#now = deps.now ?? Date.now;
		this.boards = tokens.map((token) => {
			const client = deps.client(token);
			this.#clients[token] = client;
			return {
				token,
				status: 'loading',
				revision: -1n,
				failing: [],
				source: kioskSource(client, () => this.invalidate(token))
			};
		});
	}

	/** The boards with a dashboard to show. */
	get shown(): Board[] {
		return this.boards.filter((b) => b.spec && b.status !== 'invalid');
	}

	/** The board on screen. */
	get current(): Board | undefined {
		const shown = this.shown;
		return shown.length === 0 ? undefined : shown[this.index % shown.length];
	}

	/** Nothing to show and nothing to wait for: every token is invalid. */
	get allInvalid(): boolean {
		return this.boards.length > 0 && this.boards.every((b) => b.status === 'invalid');
	}

	#update(token: string, change: (b: Board) => Board) {
		this.boards = this.boards.map((b) => (b.token === token ? change(b) : b));
	}

	/** Loads every board's dashboard again (also the reconnect after an outage). */
	async refresh(): Promise<void> {
		await Promise.all(this.boards.map((b) => this.#refresh(b.token)));
	}

	async #refresh(token: string): Promise<void> {
		const client = this.#clients[token];
		if (!client || this.boards.find((b) => b.token === token)?.status === 'invalid') return;
		try {
			const res = await client.getKioskDashboard({});
			this.#update(token, (b) => ({
				...b,
				status: 'ready',
				spec: res.revision === b.revision && b.spec ? b.spec : res.spec,
				revision: res.revision,
				staleSince: b.failing.length > 0 ? b.staleSince : undefined
			}));
		} catch (error) {
			if (ConnectError.from(error).code === Code.Unauthenticated) this.invalidate(token);
			else
				this.#update(token, (b) => ({
					...b,
					status: 'offline',
					staleSince: b.staleSince ?? this.#now()
				}));
		}
	}

	/** A widget of the board loaded (ok) or failed. */
	widgetStatus(token: string, widgetId: string, ok: boolean): void {
		const board = this.boards.find((b) => b.token === token);
		// Nothing changes: keep the boards (every widget reports after every load).
		if (!board || (ok && !board.failing.includes(widgetId))) return;
		this.#update(token, (b) => {
			const failing = ok
				? b.failing.filter((id) => id !== widgetId)
				: b.failing.includes(widgetId)
					? b.failing
					: [...b.failing, widgetId];
			const stale = failing.length > 0 || b.status === 'offline';
			return { ...b, failing, staleSince: stale ? (b.staleSince ?? this.#now()) : undefined };
		});
	}

	/** The token opens nothing any more: forget its dashboard. */
	invalidate(token: string): void {
		this.#update(token, (b) => ({
			...b,
			status: 'invalid',
			spec: undefined,
			failing: [],
			staleSince: undefined
		}));
	}

	/** The next board that can be shown. */
	rotate(): void {
		const n = this.shown.length;
		this.index = n === 0 ? 0 : (this.index + 1) % n;
	}
}
