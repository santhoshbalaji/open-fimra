WITH grants AS MATERIALIZED (
    SELECT * FROM alert_grants WHERE oidc_subject=$1 AND slug=$2
), bounds AS (
    SELECT now()-make_interval(hours=>$3) AS start_at,
           date_trunc('day',now(),'Asia/Kolkata') AS midnight
), matches AS (
    SELECT m.observed_at,m.forest_class
    FROM detection_matches m CROSS JOIN bounds
    WHERE m.observed_at BETWEEN least(start_at,midnight) AND now()
      AND ($4='' OR m.forest_class=$4) AND ($5::ltree IS NULL OR m.unit_paths <@ $5)
      AND EXISTS (SELECT FROM grants g WHERE g.workspace_id=m.workspace_id
        AND match_under_grant(m.hierarchy_unit_paths,g.hierarchy_id,g.unit_path,g.depth))
), classes AS (
    SELECT forest_class, count(*) FILTER (WHERE observed_at>=start_at) AS in_window,
           count(*) FILTER (WHERE observed_at>=midnight) AS today
    FROM matches CROSS JOIN bounds GROUP BY forest_class
), visible AS MATERIALIZED (
    SELECT a.id,a.queue,i.current_step,s.terminal,s.report_pending,
           EXISTS (SELECT FROM workflow_transitions t
              WHERE (t.template_id,t.from_step)=(i.template_id,i.current_step)
                AND now()>i.entered_at+t.time_limit) AS overdue
    FROM alerts a
    JOIN workflow_instances i ON i.alert_id=a.id
    JOIN workflow_steps s ON (s.template_id,s.slug)=(i.template_id,i.current_step)
    WHERE a.observed_at BETWEEN (SELECT start_at FROM bounds) AND now()
      AND ($4='' OR a.forest_class=$4) AND ($5::ltree IS NULL OR a.unit_paths <@ $5)
      AND EXISTS (
          SELECT FROM grants g WHERE g.workspace_id=a.workspace_id
            AND (a.queue='unit' OR g.can_review)
            AND match_under_grant(a.hierarchy_unit_paths,g.hierarchy_id,g.unit_path,g.depth)
      )
), times AS (
    SELECT milestone, percentile_cont(ARRAY[0.5,0.9]) WITHIN GROUP (ORDER BY seconds) AS seconds
    FROM visible v
    CROSS JOIN LATERAL (
        SELECT to_acknowledged,to_reached_site,to_controlled
        FROM alert_response_times WHERE alert_id=v.id
    ) r
    CROSS JOIN LATERAL (VALUES ('acknowledged',extract(epoch FROM to_acknowledged)),
       ('reached_site',extract(epoch FROM to_reached_site)),('controlled',extract(epoch FROM to_controlled))) t(milestone,seconds)
    GROUP BY milestone
)
SELECT EXISTS (SELECT FROM grants), (SELECT workspace_id::text FROM grants WHERE depth=0 LIMIT 1),
    (SELECT coalesce(jsonb_object_agg(forest_class,in_window),'{}') FROM classes),
    (SELECT coalesce(jsonb_object_agg(forest_class,today),'{}') FROM classes),
    (SELECT coalesce(jsonb_object_agg(queue,n),'{}') FROM (SELECT queue,count(*) n FROM visible GROUP BY queue) q),
    (SELECT coalesce(jsonb_object_agg(current_step,n),'{}') FROM (SELECT current_step,count(*) n FROM visible GROUP BY current_step) q),
    count(*) FILTER (WHERE NOT terminal),count(*) FILTER (WHERE overdue),count(*) FILTER (WHERE report_pending),
    (SELECT coalesce(jsonb_object_agg(milestone,jsonb_build_object('median',seconds[1],'p90',seconds[2])),'{}') FROM times)
FROM visible
