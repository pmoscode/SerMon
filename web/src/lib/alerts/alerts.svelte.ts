import { api, Code, codeOf } from '#lib/api/client.js';
import type { Alert, AlertHistoryEntry, AlertKey } from '#lib/api/gen/sermon/web/v1/alert_pb.js';
import { m } from '#lib/paraglide/messages.js';
import { needsStepUp, withStepUp } from '#lib/stores/stepup.svelte.js';

/** How often the list refreshes (the engine evaluates every 10 s). */
export const REFRESH_INTERVAL = 15_000;

/** The active alerts of the alert page and their refresh. */
export class AlertsState {
	alerts = $state<Alert[]>([]);
	status = $state<'loading' | 'ready' | 'error'>('loading');
	/** The last refresh failed while older data is still shown. */
	stale = $state(false);
	updatedAt = $state<number | undefined>(undefined);

	async load(): Promise<void> {
		try {
			this.alerts = (await api.alerts.listAlerts({})).alerts;
			this.status = 'ready';
			this.stale = false;
			this.updatedAt = Date.now();
		} catch (error) {
			// An ended session is handled by the expiry interceptor (login page).
			if (codeOf(error) === Code.Unauthenticated) return;
			if (this.status === 'ready') this.stale = true;
			else this.status = 'error';
		}
	}

	/** Loads now and keeps refreshing; returns the function that stops it. */
	start(): () => void {
		void this.load();
		const timer = setInterval(() => void this.load(), REFRESH_INTERVAL);
		return () => clearInterval(timer);
	}

	/**
	 * Acknowledges an alert (asking for the password where the server wants a step-up) and
	 * reloads; returns the message of a failure, empty on success or a cancelled step-up.
	 */
	async acknowledge(key: AlertKey): Promise<string> {
		try {
			await withStepUp(() => api.alerts.acknowledgeAlert({ key }));
			await this.load();
			return '';
		} catch (error) {
			if (needsStepUp(error)) return '';
			switch (codeOf(error)) {
				case Code.FailedPrecondition:
				case Code.NotFound:
					await this.load();
					return m.alert_error_not_firing();
				case Code.PermissionDenied:
					return m.alert_error_forbidden();
				default:
					return m.error_unavailable();
			}
		}
	}
}

/** The history of an instance, newest first. */
export async function loadHistory(key: AlertKey): Promise<AlertHistoryEntry[]> {
	return (await api.alerts.getAlertHistory({ key })).entries;
}
