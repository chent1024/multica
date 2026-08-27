export type NotificationGroupKey =
  | "assignments"
  | "status_changes"
  | "comments"
  | "mentions"
  | "updates"
  | "agent_activity"
  | "local_delivery"
  | "system_notifications";

export type NotificationGroupValue = "all" | "muted" | "action_required";

export type NotificationPreferences = Partial<Record<NotificationGroupKey, NotificationGroupValue>>;

export interface NotificationPreferenceResponse {
  workspace_id: string;
  preferences: NotificationPreferences;
}
