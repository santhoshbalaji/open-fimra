-- Add synthetic notices to the seeded demo workspace for the inbox walkthrough.
BEGIN;

INSERT INTO feed_health_alerts(workspace_id,source,product,opened_status,status,opened_at,last_checked_at)
SELECT id,'inbox_demo','VIIRS_SNPP_NRT','quiet','quiet',now(),now()
FROM workspaces WHERE slug='demo'
ON CONFLICT (workspace_id,source,product) WHERE resolved_at IS NULL DO NOTHING;

UPDATE notifications n SET read_at=NULL
FROM feed_health_alerts f, workspaces w, users u
WHERE n.feed_alert_id=f.id AND n.workspace_id=w.id AND n.recipient_id=u.id
  AND w.slug='demo' AND u.username='state.admin' AND f.source='inbox_demo' AND n.channel='in_app';

WITH sample_alert AS (
    SELECT n.workspace_id,n.recipient_id,array_agg(n.alert_id ORDER BY n.id DESC) AS alert_ids
    FROM notifications n JOIN workspaces w ON w.id=n.workspace_id JOIN users u ON u.id=n.recipient_id
    WHERE w.slug='demo' AND u.username='state.admin' AND n.event='alert_new' AND n.event_id>0
      AND n.channel='in_app' AND notification_visible(n)
    GROUP BY n.workspace_id,n.recipient_id
)
INSERT INTO notifications(workspace_id,event,event_id,alert_id,recipient_id,channel,status,created_at)
SELECT workspace_id,'alert_new',-i,alert_ids[1+(i-1)%cardinality(alert_ids)],recipient_id,
       'in_app','delivered',now()-(15-i)*interval '1 minute'
FROM sample_alert CROSS JOIN generate_series(1,14) i
ON CONFLICT (workspace_id,event,event_id,recipient_id,channel)
DO UPDATE SET alert_id=EXCLUDED.alert_id,created_at=EXCLUDED.created_at,read_at=NULL;

COMMIT;
