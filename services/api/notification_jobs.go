package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

//go:embed daily_return_notices.sql
var dailyReturnNoticesSQL string

const (
	notificationQueue    = "notifications"
	notificationInterval = 15 * time.Second
	maxEmailAttempts     = 5
)

type notificationArgs struct{}

func (notificationArgs) Kind() string { return notificationQueue }

type notificationWorker struct {
	river.WorkerDefaults[notificationArgs]
	db    *pgxpool.Pool
	email notificationChannel
	push  pushSettings
}

type notificationMessage struct {
	id                            int64
	attempt                       int
	workspaceID, workspace, event string
	alertID                       *int64
	address                       *string
	dailyDay, dailyUnit           *string
}

// Each channel decides whether the outcome can be retried safely.
type notificationChannel interface {
	deliver(context.Context, notificationMessage) (string, error)
}

// Text messages stay out of the job queue until a deployment supplies a gateway.
type smsNotificationChannel struct{}

func (smsNotificationChannel) deliver(context.Context, notificationMessage) (string, error) {
	return "failed", errors.New("text messages require an agency gateway and registered templates")
}

func checkOverdueNotifications(ctx context.Context, db ingestDB, now time.Time) error {
	_, err := db.Exec(ctx, `
WITH due AS MATERIALIZED (
    SELECT i.alert_id, l.id AS step_id
    FROM workflow_instances i
    CROSS JOIN LATERAL (SELECT id FROM step_log WHERE alert_id=i.alert_id
        AND to_step=i.current_step AND occurred_at=i.entered_at ORDER BY id DESC LIMIT 1) l
    WHERE NOT workflow_template_extended(i.template_id)
      AND EXISTS (SELECT FROM workflow_transitions t WHERE t.template_id=i.template_id
        AND t.from_step=i.current_step AND $1 > i.entered_at+t.time_limit)
      AND NOT EXISTS (SELECT FROM notification_overdue_steps WHERE step_id=l.id)
    ORDER BY i.entered_at, i.alert_id LIMIT 500 FOR UPDATE OF i SKIP LOCKED
), opened AS (
    INSERT INTO notification_overdue_steps SELECT step_id FROM due ON CONFLICT DO NOTHING RETURNING step_id
)
SELECT enqueue_alert_notifications(a, 'alert_overdue', o.step_id)
FROM opened o JOIN due d USING (step_id) JOIN alerts a ON a.id=d.alert_id`, now)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `
WITH due AS MATERIALIZED (
    SELECT i.incident_id,l.id AS step_id
    FROM workflow_instances i JOIN incidents c ON c.id=i.incident_id AND c.merged_into IS NULL
    CROSS JOIN LATERAL (SELECT id FROM step_log WHERE incident_id=i.incident_id
        AND to_step=i.current_step ORDER BY id DESC LIMIT 1) l
    WHERE workflow_template_extended(i.template_id)
      AND EXISTS (SELECT FROM incident_response_target(c.workspace_id,i.template_id,i.current_step,i.entered_at) t
                  WHERE t.due_at < $1 AND NOT EXISTS (SELECT FROM incident_overdue_escalations n WHERE n.step_id=l.id AND n.level=t.level))
    ORDER BY i.entered_at,i.incident_id LIMIT 500 FOR UPDATE OF i SKIP LOCKED
), opened AS (
    INSERT INTO incident_overdue_escalations(step_id,level,sent_at)
    SELECT d.step_id,t.level,$1 FROM due d JOIN workflow_instances i USING (incident_id)
    JOIN incidents c ON c.id=i.incident_id
    CROSS JOIN LATERAL incident_response_target(c.workspace_id,i.template_id,i.current_step,i.entered_at) t
    WHERE t.due_at < $1
    ON CONFLICT DO NOTHING RETURNING id,step_id,level
)
SELECT enqueue_alert_notifications(a,CASE o.level WHEN 1 THEN 'incident_overdue' ELSE 'incident_escalated' END,o.id)
FROM opened o JOIN due d USING (step_id)
JOIN incident_members m ON m.incident_id=d.incident_id JOIN alerts a ON a.id=m.alert_id`, now)
	return err
}

func (w *notificationWorker) Timeout(*river.Job[notificationArgs]) time.Duration {
	return 2 * time.Minute
}

func (w *notificationWorker) Work(ctx context.Context, _ *river.Job[notificationArgs]) error {
	if err := checkOverdueNotifications(ctx, w.db, time.Now()); err != nil {
		return fmt.Errorf("check overdue alerts: %w", err)
	}
	if _, err := w.db.Exec(ctx, dailyReturnNoticesSQL, time.Now()); err != nil {
		return fmt.Errorf("check daily return cutoffs: %w", err)
	}
	if err := w.deliverEmails(ctx); err != nil {
		return err
	}
	return w.deliverPushes(ctx)
}

func (w *notificationWorker) deliverEmails(ctx context.Context) error {
	if w.email == nil {
		return nil
	}
	// The claim is committed before SMTP. A worker that disappears may have
	// sent the mail already, so its abandoned attempt cannot be retried safely.
	if _, err := w.db.Exec(ctx, `
WITH stale AS (
    UPDATE notifications SET status='uncertain' WHERE channel='email' AND status='sending'
      AND next_attempt_at <= now() RETURNING id, attempts
)
UPDATE notification_attempts a SET status='uncertain', finished_at=clock_timestamp(), error='Worker stopped during delivery'
FROM stale s WHERE (a.notification_id,a.attempt)=(s.id,s.attempts)`); err != nil {
		return fmt.Errorf("close abandoned notification attempts: %w", err)
	}
	for range 10 {
		found, err := w.deliverNextEmail(ctx)
		if err != nil || !found {
			return err
		}
	}
	return nil
}

func (w *notificationWorker) deliverNextEmail(ctx context.Context) (bool, error) {
	var m notificationMessage
	err := w.db.QueryRow(ctx, `
WITH ready AS MATERIALIZED (
    SELECT n.*, w.slug, CASE WHEN NOT u.notification_email_muted AND notification_visible(n)
                            THEN u.notification_email END AS address
    FROM notifications n JOIN users u ON u.id=n.recipient_id JOIN workspaces w ON w.id=n.workspace_id
    WHERE n.channel='email' AND n.status='pending' AND n.next_attempt_at<=now()
    ORDER BY n.next_attempt_at,n.id LIMIT 1 FOR UPDATE OF n SKIP LOCKED
), claimed AS (
    UPDATE notifications n SET status=CASE WHEN r.address IS NULL THEN 'skipped' ELSE 'sending' END,
        attempts=n.attempts+(r.address IS NOT NULL)::integer,
        next_attempt_at=now()+interval '5 minutes'
    FROM ready r WHERE n.id=r.id
    RETURNING n.id,n.attempts,n.workspace_id,n.event,n.alert_id,r.slug,r.address,n.recipient_id
), attempt AS (
    INSERT INTO notification_attempts(notification_id,attempt,status)
    SELECT id,attempts,'sending' FROM claimed WHERE address IS NOT NULL
), audited AS (
    INSERT INTO audit_log(workspace_id,actor_kind,action,target_type,target_id)
    SELECT workspace_id,'system','notification.email_access','user',recipient_id::text
    FROM claimed WHERE address IS NOT NULL
)
SELECT c.id,c.attempts,c.workspace_id::text,c.slug,c.event,c.alert_id,c.address,d.day::text,d.unit_path::text
FROM claimed c LEFT JOIN notifications n ON n.id=c.id LEFT JOIN daily_return_notices d ON d.id=n.daily_notice_id`).
		Scan(&m.id, &m.attempt, &m.workspaceID, &m.workspace, &m.event, &m.alertID, &m.address, &m.dailyDay, &m.dailyUnit)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim notification email: %w", err)
	}
	if m.address == nil {
		return true, nil
	}

	status, deliveryErr := w.email.deliver(ctx, m)
	notificationStatus := status
	delay := time.Duration(0)
	if status == "retry" {
		if m.attempt >= maxEmailAttempts {
			status, notificationStatus = "failed", "failed"
		} else {
			notificationStatus = "pending"
			delay = 30 * time.Second << (m.attempt - 1)
		}
	}
	var reason *string
	if deliveryErr != nil {
		safeReason := deliveryErr.Error()
		reason = &safeReason
	}
	// Record the result even if shutdown cancelled the job during SMTP.
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, err = w.db.Exec(finish, `
WITH finished AS (
    UPDATE notifications SET status=$3, next_attempt_at=now()+$4::bigint*interval '1 second'
    WHERE id=$1 AND attempts=$2 AND status='sending' RETURNING id
)
UPDATE notification_attempts a SET status=$5, finished_at=clock_timestamp(), error=$6
FROM finished f WHERE a.notification_id=f.id AND a.attempt=$2`,
		m.id, m.attempt, notificationStatus, int64(delay/time.Second), status, reason)
	if err != nil {
		return false, fmt.Errorf("finish notification email attempt: %w", err)
	}
	return true, nil
}
