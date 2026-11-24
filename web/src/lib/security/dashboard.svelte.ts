import { timestampFromMs } from '@bufbuild/protobuf/wkt';
import { Code, codeOf } from '#lib/api/client.js';
import type {
	GetSecurityOverviewResponse,
	SecurityEvent
} from '#lib/api/gen/sermon/web/v1/security_event_pb.js';
import { securityApi } from '#lib/api/security.js';
import { saveFile } from '#lib/download/download.js';
import { formatNumber } from '#lib/format/format.js';
import { localeState } from '#lib/i18n/locale.svelte.js';
import { m } from '#lib/paraglide/messages.js';
import { securityRange, type SecurityFilter } from './filter.js';

/** Events of one page of the table. */
export const PAGE = 100;

/** The message for a failed request. */
export function securityFailure(error: unknown): string {
	switch (codeOf(error)) {
		case Code.PermissionDenied:
			return m.secdash_error_forbidden();
		case Code.InvalidArgument:
			return m.secdash_error_invalid();
		default:
			return m.error_unavailable();
	}
}

/** The filter as the API takes it; a fixed node replaces the node filter. */
export function eventFilter(
	f: SecurityFilter,
	range: { from: number; to: number },
	fixedNode?: string
) {
	return {
		from: timestampFromMs(range.from),
		to: timestampFromMs(range.to),
		nodeIds: fixedNode ? [fixedNode] : f.nodes,
		kinds: f.kinds,
		user: f.user,
		network: f.network,
		newSourcesOnly: f.newOnly
	};
}

type Status = 'loading' | 'ready' | 'error';

/**
 * The state of a security dashboard: the overview of the range (chart, top lists, totals) and
 * the events of the filter page by page, with the CSV export of the same filter.
 */
export class SecurityDashboardState {
	overview = $state.raw<GetSecurityOverviewResponse | undefined>(undefined);
	overviewStatus = $state<Status>('loading');
	events = $state.raw<SecurityEvent[]>([]);
	eventsStatus = $state<Status>('loading');
	error = $state('');
	next = $state('');
	loadingMore = $state(false);
	exporting = $state(false);
	/** The range of the last load. */
	range = $state.raw({ from: 0, to: 0 });

	#filter: SecurityFilter | undefined;
	#fixedNode: string | undefined;
	#ctrl: AbortController | undefined;

	/** Loads the overview and the first page of the filter. */
	async load(filter: SecurityFilter, fixedNode?: string, now = Date.now()): Promise<void> {
		this.#ctrl?.abort();
		const ctrl = new AbortController();
		this.#ctrl = ctrl;
		this.#filter = filter;
		this.#fixedNode = fixedNode;
		const range = securityRange(filter, now);
		this.range = range;
		this.overviewStatus = 'loading';
		this.eventsStatus = 'loading';
		this.error = '';
		const request = eventFilter(filter, range, fixedNode);
		const overview = securityApi.events
			.getSecurityOverview(
				{ from: request.from, to: request.to, nodeIds: request.nodeIds },
				{ signal: ctrl.signal }
			)
			.then((res) => {
				if (ctrl.signal.aborted) return;
				this.overview = res;
				this.overviewStatus = 'ready';
			})
			.catch((error: unknown) => {
				if (ctrl.signal.aborted || codeOf(error) === Code.Unauthenticated) return;
				this.overview = undefined;
				this.overviewStatus = 'error';
				this.error ||= securityFailure(error);
			});
		const events = securityApi.events
			.listSecurityEvents({ ...request, pageSize: PAGE }, { signal: ctrl.signal })
			.then((res) => {
				if (ctrl.signal.aborted) return;
				this.events = res.events;
				this.next = res.nextPageToken;
				this.eventsStatus = 'ready';
			})
			.catch((error: unknown) => {
				if (ctrl.signal.aborted || codeOf(error) === Code.Unauthenticated) return;
				this.events = [];
				this.next = '';
				this.eventsStatus = 'error';
				this.error ||= securityFailure(error);
			});
		await Promise.all([overview, events]);
	}

	/** Loads the next page of events. */
	async loadMore(): Promise<void> {
		if (!this.#filter || !this.next || this.loadingMore) return;
		this.loadingMore = true;
		try {
			const res = await securityApi.events.listSecurityEvents({
				...eventFilter(this.#filter, this.range, this.#fixedNode),
				pageSize: PAGE,
				pageToken: this.next
			});
			this.events = [...this.events, ...res.events];
			this.next = res.nextPageToken;
		} catch (error) {
			if (codeOf(error) !== Code.Unauthenticated) this.error = securityFailure(error);
		} finally {
			this.loadingMore = false;
		}
	}

	/** Downloads the events of the filter as CSV; the message for the user. */
	async exportCSV(): Promise<string> {
		if (!this.#filter || this.exporting) return '';
		this.exporting = true;
		try {
			const res = await securityApi.events.exportSecurityEvents({
				filter: eventFilter(this.#filter, this.range, this.#fixedNode)
			});
			saveFile(res.content as Uint8Array<ArrayBuffer>, res.fileName, 'text/csv;charset=utf-8');
			const count = formatNumber(res.rows, localeState.current);
			return res.truncated
				? m.secdash_export_truncated({ count })
				: m.secdash_export_done({ count });
		} catch (error) {
			return codeOf(error) === Code.Unauthenticated ? '' : securityFailure(error);
		} finally {
			this.exporting = false;
		}
	}

	destroy(): void {
		this.#ctrl?.abort();
	}
}
