/**
 * Client of the fleet heatmap (M4-27). Apart from #lib/api/client.js, so that other pages don't
 * load its descriptors.
 */
import { client } from './client.js';
import { HeatmapService } from './gen/sermon/web/v1/heatmap_pb.js';

export const heatmapApi = client(HeatmapService);
