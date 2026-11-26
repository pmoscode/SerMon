import { timestampMs } from '@bufbuild/protobuf/wkt';
import type { MetricSeries } from '#lib/api/gen/sermon/web/v1/metrics_pb.js';
import type { WidgetSeries } from './source.js';

/** The series of the readers with the host names of the nodes. */
export function seriesOf(
	series: MetricSeries[],
	nodes: { id: string; hostname: string }[]
): WidgetSeries[] {
	const names = new Map(nodes.map((n) => [n.id, n.hostname]));
	return series.map((s) => ({
		nodeId: s.nodeId,
		hostname: names.get(s.nodeId) ?? '',
		labels: { ...s.labels },
		points: s.points.map((p) => ({
			time: p.time ? timestampMs(p.time) : 0,
			value: p.avg,
			min: p.min,
			max: p.max
		}))
	}));
}
