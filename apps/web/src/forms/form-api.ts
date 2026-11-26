import type { HttpErrorResponse } from '@angular/common/http';
import type { AlertFacts } from '@open-fimra/form-schema/answers';
import type { AlertRow } from '../alert-queue';

/** A published form version, as GET …/forms/{form}/versions/{version} returns it. */
export interface FormVersion {
  form: string;
  version: number;
  status: string;
  definition: Record<string, unknown>;
}

export interface Approval { decision: 'approved' | 'returned'; note: string; by: string; at: string }

/** One field report, as the core API returns it. */
export interface Submission {
  id: string;
  alertId: string;
  incidentId: string | null;
  form: string;
  version: number;
  step: string;
  submittedBy: string;
  deviceTime: string | null;
  receivedAt: string;
  location: { longitude: number; latitude: number } | null;
  values: Record<string, unknown>;
  approval: Approval | null;
  /** Whether the caller is a supervising officer on the alert. */
  canApprove?: boolean;
}

/** A form assigned to an alert's workflow, from GET …/alerts/{id}/forms. */
export interface AssignedForm {
  form: string;
  version: number;
  title: string;
  step: string;
  submitted: boolean;
  requiredNow: boolean;
}

/** A row of the submissions list. */
export interface SubmissionRow {
  id: string;
  form: string;
  version: number;
  title: Record<string, string>;
  step: string;
  step_label: string;
  alert_id: string;
  incident_id: string | null;
  alert_status: string;
  alert_step_label: string;
  queue: string;
  forest_class: string;
  observed_at: number;
  beat_name: string | null;
  range_name: string | null;
  division_name: string | null;
  submitted_by: string;
  received_time: string;
  device_time: string | null;
  decision: Decision;
  decided_at: string | null;
  decided_by: string | null;
  decision_note: string | null;
  can_approve: boolean;
}

export type Decision = 'pending' | 'approved' | 'returned';

export const decisionLabels: Record<Decision, string> = {
  pending: 'Awaiting review', approved: 'Approved', returned: 'Returned'
};

/** A form in the workspace, from GET …/forms. */
export interface WorkspaceForm {
  form: string;
  versions: { version: number; status: string; title?: Record<string, string> }[];
}

/** The alert values a form copies, from an alert queue row. */
export function alertFacts(row: AlertRow & { created_at?: string }): AlertFacts {
  return {
    id: row.id,
    observedAt: new Date(row.observed_at * 1000).toISOString(),
    raisedAt: row.created_at ?? new Date(row.observed_at * 1000).toISOString(),
    longitude: row.longitude,
    latitude: row.latitude,
    unit: row.beat_name ?? null,
    forestClass: row.forest_class,
    source: row.source ?? null,
    confidence: row.confidence === undefined || row.confidence === null ? null : String(row.confidence),
    firePower: typeof row.frp_mw === 'number' ? row.frp_mw : null
  };
}

/** The core API's message for a failed request, and its list of problems. */
export function serverProblems(error: HttpErrorResponse): { message: string; problems: string[] } {
  const body = error.error as { error?: unknown; problems?: unknown } | null;
  const message = typeof body?.error === 'string' ? body.error : '';
  const problems = Array.isArray(body?.problems) ? body.problems.filter((p): p is string => typeof p === 'string') : [];
  if (error.status === 0) return { message: 'Could not reach the server. Check the connection and try again.', problems };
  if (error.status === 413) return { message: message || 'The file is too large to upload.', problems };
  return { message: message || 'The server could not handle the request. Try again.', problems };
}

/** Readable file sizes. */
export function fileSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} bytes`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}
