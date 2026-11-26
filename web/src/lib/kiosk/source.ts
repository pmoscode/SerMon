/** The widgets' data of a display: the KioskService builds every query from the stored widget. */
import { Code, ConnectError } from '@connectrpc/connect';
import type { Widget } from '#lib/api/gen/sermon/web/v1/dashboard_pb.js';
import { seriesOf } from '#lib/dashboards/series.js';
import { unitOf, type WidgetResult, type WidgetSource } from '#lib/dashboards/source.js';
import type { KioskClient } from './api.js';

export function kioskSource(client: KioskClient, onInvalid: () => void): WidgetSource {
	return {
		async load(w: Widget): Promise<WidgetResult> {
			try {
				const res = await client.queryKioskWidget({ widgetId: w.id });
				const data = res.data;
				if (data.case === 'logs')
					return { kind: 'logs', lines: data.value.lines, more: data.value.more };
				if (data.case === 'alerts') return { kind: 'alerts', alerts: data.value.alerts };
				const m = data.case === 'metrics' ? data.value : undefined;
				const names = Object.entries(m?.hostnames ?? {}).map(([id, hostname]) => ({
					id,
					hostname
				}));
				return {
					kind: 'metrics',
					unit: unitOf(m?.unit ?? ''),
					series: seriesOf(m?.series ?? [], names)
				};
			} catch (error) {
				if (ConnectError.from(error).code === Code.Unauthenticated) onInvalid();
				throw error;
			}
		}
	};
}
