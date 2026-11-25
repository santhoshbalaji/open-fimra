-- The dashboard counts. This follows scope.sql, which defines grants, bounds,
-- matches and visible.
, classes AS (
    SELECT forest_class, count(*) FILTER (WHERE observed_at>=start_at) AS in_window,
           count(*) FILTER (WHERE observed_at>=midnight) AS today
    FROM matches CROSS JOIN bounds GROUP BY forest_class
), times AS (
    SELECT milestone, percentile_cont(ARRAY[0.5,0.9]) WITHIN GROUP (ORDER BY seconds) AS seconds,
           count(seconds) AS reached
    FROM visible v
    CROSS JOIN LATERAL (
        SELECT to_alerted,to_acknowledged,to_team_sent,to_reached_site,to_controlled,to_extinguished
        FROM alert_response_times WHERE alert_id=v.id
    ) r
    CROSS JOIN LATERAL (VALUES ('alerted',extract(epoch FROM to_alerted)),
       ('acknowledged',extract(epoch FROM to_acknowledged)),('team_sent',extract(epoch FROM to_team_sent)),
       ('reached_site',extract(epoch FROM to_reached_site)),('controlled',extract(epoch FROM to_controlled)),
       ('extinguished',extract(epoch FROM to_extinguished))) t(milestone,seconds)
    GROUP BY milestone
)
SELECT EXISTS (SELECT FROM grants), (SELECT workspace_id::text FROM grants WHERE depth=0 LIMIT 1),
    (SELECT coalesce(jsonb_object_agg(forest_class,in_window),'{}') FROM classes),
    (SELECT coalesce(jsonb_object_agg(forest_class,today),'{}') FROM classes),
    (SELECT coalesce(jsonb_object_agg(queue,n),'{}') FROM (SELECT queue,count(*) n FROM visible GROUP BY queue) q),
    (SELECT coalesce(jsonb_object_agg(current_step,n),'{}') FROM (SELECT current_step,count(*) n FROM visible GROUP BY current_step) q),
    count(*) FILTER (WHERE NOT terminal),count(*) FILTER (WHERE overdue),count(*) FILTER (WHERE report_pending),
    (SELECT coalesce(jsonb_object_agg(milestone,jsonb_build_object('median',seconds[1],'p90',seconds[2],'count',reached)),'{}') FROM times)
FROM visible
