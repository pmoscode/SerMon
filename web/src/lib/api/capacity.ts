/**
 * Client of the capacity planning (M4-26). Apart from #lib/api/client.js, so that other pages
 * don't load its descriptors.
 */
import { client } from './client.js';
import { CapacityService } from './gen/sermon/web/v1/capacity_pb.js';

export const capacityApi = client(CapacityService);
