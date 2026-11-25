package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

func (a api) notifications(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	subject := r.Context().Value(identityKey{}).(identity).Subject
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	if raw := chi.URLParam(r, "id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id < 1 {
			writeError(w, 400, "Invalid notification ID.")
			return
		}
		tag, err := a.reader.Exec(ctx, `
UPDATE notifications n SET read_at=COALESCE(n.read_at,now()) FROM users u
WHERE n.id=$2 AND n.recipient_id=u.id AND u.oidc_subject=$1 AND n.channel='in_app' AND notification_visible(n)`, subject, id)
		if err != nil {
			a.notificationError(w, err)
		} else if tag.RowsAffected() == 0 {
			writeError(w, 404, "Notification not found.")
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
		return
	}

	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			writeError(w, 400, "Invalid notification offset.")
			return
		}
		offset = value
	}
	var result json.RawMessage
	err := a.reader.QueryRow(ctx, `
WITH inbox AS MATERIALIZED (
    SELECT n.id,n.event,n.created_at,n.read_at,w.slug AS workspace,n.alert_id
    FROM users u JOIN notifications n ON n.recipient_id=u.id JOIN workspaces w ON w.id=n.workspace_id
    WHERE u.oidc_subject=$1 AND n.channel='in_app' AND notification_visible(n)
), page AS (
    SELECT * FROM inbox ORDER BY read_at IS NOT NULL,id DESC LIMIT 50 OFFSET $2
)
SELECT jsonb_build_object('unreadCount',(SELECT count(*) FROM inbox WHERE read_at IS NULL),
    'hasMore', (SELECT count(*) FROM inbox) > $2::bigint+50,
    'items',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',page.id::text,'event',event,
        'createdAt',page.created_at,'readAt',read_at,'workspace',workspace,'alertId',page.alert_id::text,
        'forestClass',a.forest_class,'beatName',line.levels #>> '{beat,name}',
        'divisionName',line.levels #>> '{division,name}','incidentId',im.incident_id::text)
        ORDER BY read_at IS NOT NULL,page.id DESC) FROM page
        LEFT JOIN alerts a ON a.id=page.alert_id
        LEFT JOIN incident_members im ON im.alert_id=page.alert_id
        LEFT JOIN hierarchies h ON h.id=a.hierarchy_id
        LEFT JOIN LATERAL (SELECT value AS levels FROM jsonb_each(a.hierarchy)
            ORDER BY (key=h.slug) DESC,key LIMIT 1) line ON true),'[]'))`, subject, offset).Scan(&result)
	if err != nil {
		a.notificationError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func (a api) notificationPreferences(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	caller := r.Context().Value(identityKey{}).(identity)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var muted bool
	var err error
	if r.Method == http.MethodGet {
		err = a.reader.QueryRow(ctx, `
SELECT COALESCE((SELECT notification_email_muted FROM users WHERE oidc_subject=$1),false)`, caller.Subject).Scan(&muted)
	} else {
		var input struct {
			EmailMuted *bool `json:"emailMuted"`
		}
		if !decodeNotificationInput(w, r, &input) || input.EmailMuted == nil {
			writeError(w, 400, "Provide emailMuted as true or false.")
			return
		}
		err = a.reader.QueryRow(ctx, `
INSERT INTO users(oidc_subject,username,display_name,notification_email_muted) VALUES($1,$2,$3,$4)
ON CONFLICT(oidc_subject) DO UPDATE SET notification_email_muted=EXCLUDED.notification_email_muted
RETURNING notification_email_muted`, caller.Subject, caller.Username, caller.Name, *input.EmailMuted).Scan(&muted)
	}
	if err != nil {
		a.notificationError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"emailMuted": muted})
}

type notificationRule struct {
	Event       string `json:"event"`
	Recipients  string `json:"recipients"`
	Channel     string `json:"channel"`
	Queue       string `json:"queue,omitempty"`
	ForestClass string `json:"forestClass,omitempty"`
}

func (r notificationRule) valid() bool {
	return slices.Contains([]string{"alert_new", "alert_overdue", "feed_open"}, r.Event) &&
		slices.Contains([]string{"field_staff", "division_officer", "reviewers", "root"}, r.Recipients) &&
		slices.Contains([]string{"in_app", "email", "push"}, r.Channel) &&
		slices.Contains([]string{"", "unit", "edge_review", "triage"}, r.Queue) &&
		slices.Contains([]string{"", "inside", "near_edge"}, r.ForestClass) &&
		(r.Queue == "" || r.ForestClass == "") &&
		(r.Event != "feed_open" || (r.Recipients == "root" && r.Queue == "" && r.ForestClass == "" && r.Channel != "push"))
}

func (a api) notificationRules(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	subject := r.Context().Value(identityKey{}).(identity).Subject
	slug := chi.URLParam(r, "slug")
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var rules []notificationRule
	if r.Method == http.MethodPost {
		if !decodeNotificationInput(w, r, &rules) || rules == nil || len(rules) > 100 {
			writeError(w, 400, "Provide a list of up to 100 notification rules.")
			return
		}
		seen := make(map[notificationRule]bool)
		for _, rule := range rules {
			if !rule.valid() || seen[rule] {
				writeError(w, 400, "Each notification rule must be valid and appear only once.")
				return
			}
			seen[rule] = true
		}
	}

	var result json.RawMessage
	err := pgx.BeginFunc(ctx, a.reader, func(tx pgx.Tx) error {
		var workspaceID string
		query := `SELECT w.id::text FROM workspaces w WHERE w.slug=$2 AND EXISTS (
SELECT FROM alert_grants g WHERE g.workspace_id=w.id AND g.oidc_subject=$1 AND g.role_slug='state_admin' AND g.depth=0)`
		if r.Method == http.MethodPost {
			query += " FOR UPDATE OF w"
		}
		if err := tx.QueryRow(ctx, query, subject, slug).Scan(&workspaceID); err != nil {
			return err
		}
		if r.Method == http.MethodPost {
			if _, err := tx.Exec(ctx, "DELETE FROM notification_rules WHERE workspace_id=$1", workspaceID); err != nil {
				return err
			}
			body, err := json.Marshal(rules)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
INSERT INTO notification_rules(workspace_id,event,recipients,channel,queue,forest_class)
SELECT $1,event,recipients,channel,queue,"forestClass" FROM jsonb_to_recordset($2)
AS r(event text,recipients text,channel text,queue text,"forestClass" text)`, workspaceID, body); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
INSERT INTO audit_log(workspace_id,actor_kind,actor_id,action,target_type,target_id)
VALUES($1,'user',$2,'notification.rules_update','workspace',$1::uuid::text)`, workspaceID, subject); err != nil {
				return err
			}
		}
		return tx.QueryRow(ctx, `
SELECT COALESCE(jsonb_agg(jsonb_strip_nulls(jsonb_build_object('event',event,'recipients',recipients,
    'channel',channel,'queue',queue,'forestClass',forest_class))
    ORDER BY event,queue,forest_class,recipients,channel),'[]')
FROM notification_rules WHERE workspace_id=$1`, workspaceID).Scan(&result)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 403, "A workspace administrator role at the root is required.")
	} else if err != nil {
		a.notificationError(w, err)
	} else {
		writeJSON(w, 200, result)
	}
}

func decodeNotificationInput(w http.ResponseWriter, r *http.Request, input any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	decoder.DisallowUnknownFields()
	return decoder.Decode(input) == nil && decoder.Decode(new(any)) == io.EOF
}

func (a api) notificationError(w http.ResponseWriter, err error) {
	a.logger.Error("Could not process notifications", "error", err)
	writeError(w, 500, "Could not load or save notifications.")
}

func saveNotificationEmail(ctx context.Context, db ingestDB, caller identity) error {
	_, err := db.Exec(ctx, `
WITH changed AS (
    UPDATE users SET notification_email=$2 WHERE oidc_subject=$1
      AND notification_email IS DISTINCT FROM $2 RETURNING id
)
INSERT INTO audit_log(actor_kind,actor_id,action,target_type,target_id)
SELECT 'user',$1,'notification.email_update','user',id::text FROM changed`, caller.Subject, caller.Email)
	return err
}
