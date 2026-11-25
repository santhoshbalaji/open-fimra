package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	maxPushAttempts = 5
	pushRunTime     = time.Minute
)

// pushMessage is one claimed message for one device.
type pushMessage struct {
	id, notificationID int64
	attempt            int
	kind, workspace    string
	event              string
	alertID            *int64
	target             pushTarget
	deviceID           string
}

// deliverPushes sends due push messages. A message that a stopped worker left
// in sending is sent again: the app shows each notification once by its ID,
// so a repeat replaces the first instead of adding a second.
func (w *notificationWorker) deliverPushes(ctx context.Context) error {
	if _, err := w.db.Exec(ctx, `
UPDATE push_messages SET status='pending', error='Worker stopped during delivery'
WHERE status='sending' AND next_attempt_at<=now()`); err != nil {
		return fmt.Errorf("reopen abandoned push messages: %w", err)
	}
	// A burst of alerts in fire season can mean many messages. Send for up
	// to a minute per run, well inside the job's time limit; the rest wait
	// for the next run.
	deadline := time.Now().Add(pushRunTime)
	for time.Now().Before(deadline) {
		found, err := w.deliverNextPush(ctx)
		if err != nil || !found {
			return err
		}
	}
	return nil
}

func (w *notificationWorker) deliverNextPush(ctx context.Context) (bool, error) {
	var m pushMessage
	var sendable bool
	var provider, endpoint *string
	err := w.db.QueryRow(ctx, `
WITH ready AS MATERIALIZED (
    SELECT p.id, p.kind, p.notification_id, d.id AS device_id, d.push_provider, d.push_endpoint,
           d.push_public_key, d.push_auth, n.event, n.alert_id, w.slug,
           d.push_endpoint IS NOT NULL AND d.push_provider = ANY($1::text[]) AND CASE p.kind
               WHEN 'notification' THEN device_can_push(d) AND notification_visible(n)
               ELSE d.wiped_at IS NULL END AS sendable
    FROM push_messages p JOIN devices d ON d.id=p.device_id
    LEFT JOIN notifications n ON n.id=p.notification_id LEFT JOIN workspaces w ON w.id=n.workspace_id
    WHERE p.status='pending' AND p.next_attempt_at<=now()
    ORDER BY p.next_attempt_at, p.id LIMIT 1 FOR UPDATE OF p SKIP LOCKED
), claimed AS (
    UPDATE push_messages p SET status=CASE WHEN r.sendable THEN 'sending' ELSE 'skipped' END,
        attempts=p.attempts+r.sendable::integer, next_attempt_at=now()+interval '5 minutes',
        finished_at=CASE WHEN r.sendable THEN NULL ELSE now() END,
        error=CASE WHEN r.sendable THEN p.error ELSE 'The device or recipient can no longer receive it.' END
    FROM ready r WHERE p.id=r.id
    RETURNING p.id, p.attempts
)
SELECT c.id, c.attempts, r.kind, COALESCE(r.notification_id, 0), r.device_id::text, r.push_provider, r.push_endpoint,
       r.push_public_key, r.push_auth, COALESCE(r.event, ''), r.alert_id, COALESCE(r.slug, ''), r.sendable
FROM claimed c JOIN ready r USING (id)`, w.push.providers()).Scan(&m.id, &m.attempt, &m.kind, &m.notificationID,
		&m.deviceID, &provider, &endpoint, &m.target.publicKey, &m.target.auth, &m.event, &m.alertID, &m.workspace, &sendable)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim push message: %w", err)
	}
	if !sendable {
		return true, w.refreshPushNotification(ctx, m.notificationID)
	}
	m.target.provider, m.target.endpoint = *provider, *endpoint

	status, deliveryErr := w.push.sender(m.target.provider).send(ctx, m.target, marshalPushPayload(newPushPayload(m)))
	messageStatus, delay := status, time.Duration(0)
	switch status {
	case "retry":
		if m.attempt >= maxPushAttempts {
			messageStatus = "failed"
		} else {
			messageStatus = "pending"
			delay = 30 * time.Second << (m.attempt - 1)
		}
	case "gone":
		messageStatus = "failed"
	}
	var reason *string
	if deliveryErr != nil {
		safe := deliveryErr.Error()
		reason = &safe
	}
	// Record the result even if shutdown cancelled the job during delivery.
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err = pgx.BeginFunc(finish, w.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(finish, `
UPDATE push_messages SET status=$3, next_attempt_at=now()+$4::bigint*interval '1 second', error=$5,
    finished_at=CASE WHEN $3 IN ('delivered', 'failed') THEN now() END
WHERE id=$1 AND attempts=$2 AND status='sending'`, m.id, m.attempt, messageStatus, int64(delay/time.Second), reason); err != nil {
			return err
		}
		if status == "gone" {
			// The phone or its push app dropped this address. Stop using it
			// until the app registers a new one.
			if _, err := tx.Exec(finish, `
UPDATE devices SET push_provider=NULL, push_endpoint=NULL, push_public_key=NULL, push_auth=NULL
WHERE id=$1 AND push_endpoint=$2`, m.deviceID, m.target.endpoint); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("finish push message: %w", err)
	}
	return true, w.refreshPushNotification(finish, m.notificationID)
}

// refreshPushNotification sets a push notification's status from its
// messages: delivered once any device has it.
func (w *notificationWorker) refreshPushNotification(ctx context.Context, id int64) error {
	if id == 0 {
		return nil
	}
	_, err := w.db.Exec(ctx, `
UPDATE notifications n SET status=s.status FROM (
    SELECT notification_id, CASE WHEN bool_or(status='delivered') THEN 'delivered'
        WHEN bool_or(status IN ('pending', 'sending')) THEN 'pending'
        WHEN bool_or(status='failed') THEN 'failed' ELSE 'skipped' END AS status
    FROM push_messages WHERE notification_id=$1 GROUP BY notification_id
) s WHERE n.id=s.notification_id AND n.status IS DISTINCT FROM s.status`, id)
	if err != nil {
		return fmt.Errorf("update push notification status: %w", err)
	}
	return nil
}
