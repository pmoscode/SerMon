/**
 * The client of the security dashboard (KONZEPT §16.4). It lives apart from #lib/api/client.js so
 * that pages without it don't load its descriptors (JavaScript budget); tests replace it with fakes.
 */
import { client } from './client.js';
import { SecurityEventService } from './gen/sermon/web/v1/security_event_pb.js';

export const securityApi = { events: client(SecurityEventService) };
