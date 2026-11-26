/**
 * Client of the custom dashboards (M5-23). Apart from #lib/api/client.js, so that other pages don't
 * load its descriptors.
 */
import { client } from './client.js';
import { DashboardService } from './gen/sermon/web/v1/dashboard_pb.js';

export const dashboardsApi = client(DashboardService);
