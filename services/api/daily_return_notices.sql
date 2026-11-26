WITH due AS (
    SELECT p.id AS policy_id, p.workspace_id, p.hierarchy_id, day, u.path, u.name,
           CASE WHEN $1 >= b.cutoff THEN 'daily_return_missing' ELSE 'daily_return_due' END AS event
    FROM daily_return_policies p
    CROSS JOIN LATERAL (SELECT ($1::timestamptz AT TIME ZONE p.time_zone)::date AS today) local
    CROSS JOIN LATERAL (VALUES (local.today), (local.today-1)) d(day)
    CROSS JOIN LATERAL (SELECT daily_return_cutoff(day,p.cutoff,p.time_zone) AS cutoff) b
    CROSS JOIN LATERAL daily_return_units(p.hierarchy_id,p.depth,least($1,b.cutoff)) u
    WHERE p.season @> day AND $1 >= b.cutoff - interval '1 hour' AND $1 < b.cutoff + interval '24 hours'
      AND NOT EXISTS (SELECT FROM situation_reports r WHERE r.daily_policy_id=p.id
          AND r.period_from=day AND r.unit_path=u.path)
      AND EXISTS (SELECT FROM notification_rules r WHERE r.workspace_id=p.workspace_id
          AND r.event IN ('daily_return_due','daily_return_missing'))
), opened AS (
    INSERT INTO daily_return_notices(policy_id,day,unit_path,unit_name,event)
    SELECT policy_id,day,path,name,event FROM due ON CONFLICT DO NOTHING
    RETURNING *
)
INSERT INTO notifications(workspace_id,event,event_id,daily_notice_id,recipient_id,channel,status)
SELECT p.workspace_id,d.event,d.id,d.id,g.user_id,r.channel,
       CASE r.channel WHEN 'in_app' THEN 'delivered' ELSE 'pending' END
FROM opened d JOIN daily_return_policies p ON p.id=d.policy_id
JOIN notification_rules r ON r.workspace_id=p.workspace_id AND r.event=d.event
JOIN permanent_alert_grants g ON g.workspace_id=p.workspace_id
JOIN users u ON u.id=g.user_id
WHERE (g.depth=0 OR (g.hierarchy_id=p.hierarchy_id AND d.unit_path <@ g.unit_path))
  AND CASE r.recipients WHEN 'root' THEN g.depth=0 WHEN 'reviewers' THEN g.can_review
                        ELSE g.role_slug=r.recipients AND g.unit_path=d.unit_path END
  AND (r.channel='in_app' OR (u.notification_email IS NOT NULL AND NOT u.notification_email_muted))
ON CONFLICT DO NOTHING
