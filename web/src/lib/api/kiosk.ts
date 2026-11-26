/** Client of the kiosk tokens (M5-26), loaded only where admins manage them. */
import { client } from './client.js';
import { KioskTokenService } from './gen/sermon/web/v1/kiosk_pb.js';

export const kioskTokensApi = client(KioskTokenService);
