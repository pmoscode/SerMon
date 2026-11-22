import { OrganizationStatus } from '#lib/api/gen/sermon/web/v1/organization_pb.js';

/** What a superadmin can do with an organization's lifecycle (M2-33, ADR 0029). */
export type LifecycleAction = 'lock' | 'schedule' | 'unlock' | 'export' | 'delete';

/**
 * The actions the state allows: an active organization is locked or scheduled for deletion; a
 * locked one is unlocked, scheduled, exported or deleted at once; a scheduled one is unlocked
 * (which lifts the deletion), exported or deleted at once. The server checks again.
 */
export function actionsFor(status: OrganizationStatus): LifecycleAction[] {
	switch (status) {
		case OrganizationStatus.ACTIVE:
			return ['lock', 'schedule'];
		case OrganizationStatus.LOCKED:
			return ['unlock', 'schedule', 'export', 'delete'];
		case OrganizationStatus.DELETION_SCHEDULED:
			return ['unlock', 'export', 'delete'];
		default:
			return [];
	}
}

/** Whether the typed text confirms the organization: its exact name, surrounding spaces aside. */
export function confirmed(name: string, typed: string): boolean {
	return typed.trim() !== '' && typed.trim() === name;
}
