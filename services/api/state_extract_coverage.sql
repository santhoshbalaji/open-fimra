WITH days AS (
    SELECT $2::date + n AS day FROM generate_series(0, $3::date - $2::date) n
), expected AS (
    SELECT d.day, p.id AS policy_id, u.path, u.code, u.layer_version_id,
           daily_return_cutoff(d.day,p.cutoff,p.time_zone) AS cutoff
    FROM daily_return_policies p CROSS JOIN days d
    CROSS JOIN LATERAL daily_return_units(p.hierarchy_id,p.depth,daily_return_cutoff(d.day,p.cutoff,p.time_zone)) u
    WHERE p.id=$1
)
SELECT e.day::text, coalesce(r.content #>> '{dailyReturn,expectedUnit,code}',e.code),
       coalesce(r.content #>> '{dailyReturn,expectedUnit,boundaryVersion}',e.layer_version_id::text),
       r.id IS NOT NULL, coalesce((r.content #>> '{dailyReturn,nil}')::boolean,false),
       coalesce((r.content #>> '{period,end}')::timestamptz >= e.cutoff,false),
       r.content #> '{dailyReturn,stateFacts}', r.content #> '{dailyReturn,facts}'
FROM expected e LEFT JOIN LATERAL (
    SELECT r.id,r.content FROM situation_reports r JOIN daily_return_decisions d ON d.report_id=r.id
    WHERE r.daily_policy_id=e.policy_id AND r.unit_path=e.path AND r.period_from=e.day AND d.decision='approved'
    ORDER BY r.daily_revision DESC LIMIT 1
) r ON true
ORDER BY e.day,e.code,e.path
