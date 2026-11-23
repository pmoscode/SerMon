/**
 * The client of the live process view (KONZEPT §9). It lives apart from #lib/api/client.js so that
 * only the opened tab loads its descriptors (JavaScript budget); tests replace it with fakes.
 */
import { client } from './client.js';
import { LiveViewService } from './gen/sermon/web/v1/live_view_pb.js';

export const liveViewApi = { liveView: client(LiveViewService) };
