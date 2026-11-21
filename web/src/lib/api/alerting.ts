/**
 * Clients of the alerting area (alerts, rules, silences, channels, policies, maintenance windows).
 * They live apart from #lib/api/client.js so that pages without alerting don't load their
 * descriptors (JavaScript budget, M1-29); tests replace single services with fakes.
 */
import { client } from './client.js';
import { AlertService } from './gen/sermon/web/v1/alert_pb.js';
import { AlertRuleService } from './gen/sermon/web/v1/alert_rule_service_pb.js';
import { NotificationChannelService } from './gen/sermon/web/v1/notification_pb.js';
import { NotificationPolicyService } from './gen/sermon/web/v1/notification_policy_pb.js';
import { AlertSilenceService, MaintenanceWindowService } from './gen/sermon/web/v1/silence_pb.js';

export const alertingApi = {
	alerts: client(AlertService),
	rules: client(AlertRuleService),
	channels: client(NotificationChannelService),
	policies: client(NotificationPolicyService),
	silences: client(AlertSilenceService),
	windows: client(MaintenanceWindowService)
};
