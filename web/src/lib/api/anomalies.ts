/**
 * Client of the anomaly detection (M4-23, M4-25): the band of a node and "what was unusual?".
 * Apart from #lib/api/client.js, so that pages without charts don't load its descriptors.
 */
import { client } from './client.js';
import { AnomalyService } from './gen/sermon/web/v1/anomaly_pb.js';

export const anomaliesApi = client(AnomalyService);
