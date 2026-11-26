import { WidgetKind } from '#lib/api/gen/sermon/web/v1/dashboard_pb.js';
import { m } from '#lib/paraglide/messages.js';

/** A metric of the catalog as the widget form offers it. */
export interface CatalogMetric {
	name: string;
	labels: string[];
	area: string;
}

/** A node the widget form offers. */
export interface NodeChoice {
	id: string;
	hostname: string;
	groupId: string;
	groupName: string;
}

const kindLabels: Record<WidgetKind, () => string> = {
	[WidgetKind.UNSPECIFIED]: () => m.dashboard_kind_timeseries(),
	[WidgetKind.TIMESERIES]: () => m.dashboard_kind_timeseries(),
	[WidgetKind.GAUGE]: () => m.dashboard_kind_gauge(),
	[WidgetKind.STAT]: () => m.dashboard_kind_stat(),
	[WidgetKind.TABLE]: () => m.dashboard_kind_table(),
	[WidgetKind.LOG_STREAM]: () => m.dashboard_kind_log_stream(),
	[WidgetKind.ALERT_LIST]: () => m.dashboard_kind_alert_list()
};

/** The display name of a widget kind. */
export function kindLabel(kind: WidgetKind): string {
	return (kindLabels[kind] ?? kindLabels[WidgetKind.TIMESERIES])();
}
