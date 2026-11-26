-- The parts of a situation report that the dashboard does not show. This
-- follows scope.sql, so every alert here is one the dashboard counts for the
-- same window and unit. The counts themselves come from summary.sql.
-- $8 hierarchy ID of the report's unit (or NULL for all the caller's units),
-- $9 the unit's path (or NULL), $10 most alerts drawn on the map,
-- $11 most rows in each list.
, report_unit AS (
    SELECT u.hierarchy_id, u.depth, u.path, u.geom FROM units u
    WHERE u.hierarchy_id=$8::uuid AND u.current_path=$9::ltree
), place_context AS (
    -- Places are named below the report's unit, or below the caller's
    -- highest grant when the report covers all their units.
    SELECT h.id AS hierarchy_id, h.slug,
           coalesce((SELECT depth FROM report_unit), (SELECT min(depth) FROM grants g WHERE g.hierarchy_id=h.id)) AS depth
    FROM hierarchies h
    WHERE h.id=coalesce($8::uuid, (SELECT hierarchy_id FROM grants ORDER BY depth, unit_path LIMIT 1))
), place_levels AS (
    SELECT l.depth, l.slug FROM hierarchy_levels l JOIN place_context c ON c.hierarchy_id=l.hierarchy_id
    WHERE l.depth > c.depth
    ORDER BY l.depth DESC LIMIT 3
), period_alerts AS MATERIALIZED (
    SELECT v.*, a.forest_class, a.observed_at, a.hierarchy, a.hierarchy_unit_paths, d.geom, im.incident_id,
           (SELECT string_agg(a.hierarchy #>> ARRAY[c.slug, l.slug, 'name'], ' › ' ORDER BY l.depth)
              FROM place_levels l CROSS JOIN place_context c) AS place,
           EXISTS (SELECT FROM alert_forms(v.id) f WHERE f.step=v.current_step AND NOT f.submitted) AS form_missing
    FROM visible v JOIN alerts a ON a.id=v.id
    JOIN detections d ON (d.id,d.observed_at)=(a.detection_id,a.observed_at)
    LEFT JOIN incident_members im ON im.alert_id=a.id
), step_labels AS (
    SELECT DISTINCT ON (s.slug) s.slug, s.label, s.terminal
    FROM workflow_steps s JOIN workflow_templates t ON t.id=s.template_id
    WHERE t.workspace_id IS NULL OR t.workspace_id IN (SELECT workspace_id FROM grants)
    ORDER BY s.slug, t.workspace_id NULLS LAST
), incident_ids AS (
    SELECT DISTINCT incident_id AS id FROM period_alerts WHERE incident_id IS NOT NULL
), incident_alerts AS (
    -- Every member the caller may see, as in the incident view.
    SELECT m.incident_id, a.id, a.observed_at, a.id IN (SELECT id FROM period_alerts) AS in_period,
           (SELECT place FROM period_alerts p WHERE p.id=a.id) AS place
    FROM incident_ids i JOIN incident_members m ON m.incident_id=i.id JOIN alerts a ON a.id=m.alert_id
    WHERE EXISTS (SELECT FROM grants g WHERE g.workspace_id=a.workspace_id
        AND alert_path_visible(a.queue,a.hierarchy_unit_paths,g.can_review,g.hierarchy_id,g.unit_path,g.depth))
), incident_rows AS (
    SELECT i.id, wi.current_step AS step, s.label AS step_label, s.terminal,
           EXISTS (SELECT FROM workflow_transitions t WHERE t.template_id=wi.template_id
                   AND t.from_step=wi.current_step AND now()>wi.entered_at+t.time_limit) AS overdue,
           min(m.observed_at) AS first_observed, max(m.observed_at) AS last_observed,
           count(*) AS alert_count, count(*) FILTER (WHERE m.in_period) AS alerts_in_period,
           min(m.observed_at) >= (SELECT start_at FROM bounds) AS opened_in_period,
           (SELECT coalesce(jsonb_agg(DISTINCT x.place ORDER BY x.place),'[]') FROM incident_alerts x
              WHERE x.incident_id=i.id AND x.place IS NOT NULL) AS places
    FROM incidents i JOIN incident_alerts m ON m.incident_id=i.id
    JOIN workflow_instances wi ON wi.incident_id=i.id
    JOIN workflow_steps s ON (s.template_id,s.slug)=(wi.template_id,wi.current_step)
    GROUP BY i.id, wi.current_step, wi.template_id, wi.entered_at, s.label, s.terminal
), period_submissions AS (
    SELECT s.id, f.form_key, coalesce(v.definition #>> '{title,en}', f.form_key) AS title, ap.decision
    FROM submissions s JOIN period_alerts p ON p.id=s.alert_id
    JOIN form_versions v ON v.id=s.form_version_id JOIN forms f ON f.id=v.form_id
    LEFT JOIN approvals ap ON ap.submission_id=s.id
), missing_forms AS (
    SELECT p.id, f.form_key, f.title FROM period_alerts p CROSS JOIN LATERAL alert_forms(p.id) f
    WHERE p.form_missing AND f.step=p.current_step AND NOT f.submitted
), form_rows AS (
    SELECT form_key, min(title) AS title,
           count(*) FILTER (WHERE kind='submitted') AS submitted,
           count(*) FILTER (WHERE decision='approved') AS approved,
           count(*) FILTER (WHERE decision='returned') AS returned,
           count(*) FILTER (WHERE kind='submitted' AND decision IS NULL) AS awaiting_review,
           count(*) FILTER (WHERE kind='missing') AS missing
    FROM (SELECT form_key, title, decision, 'submitted' AS kind FROM period_submissions
          UNION ALL SELECT form_key, title, NULL, 'missing' FROM missing_forms) x
    GROUP BY form_key
), unit_rows AS (
    -- The units one level below the report's unit, or the caller's own units.
    SELECT u.hierarchy_id, u.depth, u.code, u.name, u.path, u.geom FROM units u
    WHERE u.valid_to IS NULL AND CASE WHEN EXISTS (SELECT FROM report_unit)
        THEN u.hierarchy_id=$8::uuid AND u.depth=(SELECT depth+1 FROM report_unit) AND u.path <@ $9::ltree
        ELSE (u.hierarchy_id,u.path) IN (SELECT hierarchy_id,unit_path FROM grants) END
    ORDER BY u.path LIMIT 500
), unit_counts AS (
    SELECT r.code, r.name, r.path,
           count(p.id) AS alerts, count(p.id) FILTER (WHERE NOT p.terminal) AS active,
           count(p.id) FILTER (WHERE p.overdue) AS overdue,
           count(p.id) FILTER (WHERE p.report_pending) AS pending_reports,
           count(p.id) FILTER (WHERE p.form_missing) AS forms_missing
    FROM unit_rows r LEFT JOIN period_alerts p
      ON match_under_grant(p.hierarchy_unit_paths, r.hierarchy_id, r.path, r.depth)
    GROUP BY r.code, r.name, r.path
), outline AS (
    SELECT coalesce((SELECT geom FROM report_unit),
                    (SELECT ST_Union(geom) FROM units u WHERE u.valid_to IS NULL
                       AND (u.hierarchy_id,u.path) IN (SELECT hierarchy_id,unit_path FROM grants))) AS geom
), map_points AS (
    SELECT id, geom, forest_class, terminal, observed_at FROM period_alerts ORDER BY observed_at DESC, id DESC LIMIT $10
), extent AS (
    SELECT ST_Extent(g) AS box FROM (SELECT geom AS g FROM outline UNION ALL SELECT geom FROM map_points) x
), tolerance AS (
    SELECT greatest(ST_XMax(box)-ST_XMin(box), ST_YMax(box)-ST_YMin(box), 0.001)/1500 AS t FROM extent
), forest AS (
    SELECT ST_SimplifyPreserveTopology(f.geom, (SELECT t FROM tolerance)) AS geom
    FROM layers l JOIN layer_versions lv ON lv.layer_id=l.id AND lv.status='published'
    JOIN layer_features f ON f.layer_version_id=lv.id
    WHERE l.role='forest' AND l.workspace_id IN (SELECT workspace_id FROM grants)
      AND f.geom && ST_SetSRID((SELECT box FROM extent)::geometry, 4326)
      AND ST_Area(f.geom) >= power((SELECT t FROM tolerance)*2, 2)
    ORDER BY ST_Area(f.geom) DESC LIMIT 2000
)
SELECT jsonb_build_object(
    'stepLabels', (SELECT coalesce(jsonb_object_agg(slug, jsonb_build_object('label',label,'terminal',terminal)),'{}') FROM step_labels),
    'placeLevels', (SELECT coalesce(jsonb_agg(l.labels->>'en' ORDER BY l.depth),'[]') FROM place_levels p
        JOIN hierarchy_levels l ON (l.hierarchy_id,l.depth)=((SELECT hierarchy_id FROM place_context),p.depth)),
    'incidents', jsonb_build_object(
        'total', (SELECT count(*) FROM incident_rows),
        'opened', (SELECT count(*) FROM incident_rows WHERE opened_in_period),
        'active', (SELECT count(*) FROM incident_rows WHERE NOT terminal),
        'closed', (SELECT count(*) FROM incident_rows WHERE terminal),
        'overdue', (SELECT count(*) FROM incident_rows WHERE overdue),
        'items', (SELECT coalesce(jsonb_agg(jsonb_build_object('id',id::text,'step',step,'stepLabel',step_label,
            'terminal',terminal,'overdue',overdue,'firstObserved',first_observed,'lastObserved',last_observed,
            'alertCount',alert_count,'alertsInPeriod',alerts_in_period,'openedInPeriod',opened_in_period,
            'places',places) ORDER BY first_observed, id),'[]')
            FROM (SELECT * FROM incident_rows ORDER BY first_observed, id LIMIT $11) i)),
    'fieldReports', jsonb_build_object(
        'submitted', (SELECT count(*) FROM period_submissions),
        'approved', (SELECT count(*) FROM period_submissions WHERE decision='approved'),
        'returned', (SELECT count(*) FROM period_submissions WHERE decision='returned'),
        'awaitingReview', (SELECT count(*) FROM period_submissions WHERE decision IS NULL),
        'missing', (SELECT count(*) FROM missing_forms),
        'alertsMissing', (SELECT count(*) FROM period_alerts WHERE form_missing),
        'forms', (SELECT coalesce(jsonb_agg(jsonb_build_object('form',form_key,'title',title,'submitted',submitted,
            'approved',approved,'returned',returned,'awaitingReview',awaiting_review,'missing',missing)
            ORDER BY title, form_key),'[]') FROM form_rows)),
    'overdueAlerts', (SELECT coalesce(jsonb_agg(jsonb_build_object('id',id::text,'step',current_step,
            'stepLabel',(SELECT label FROM step_labels WHERE slug=current_step),'enteredAt',entered_at,
            'limitSeconds',extract(epoch FROM time_limit),'overdueSeconds',extract(epoch FROM now()-entered_at-time_limit),
            'forestClass',forest_class,'observedAt',observed_at,'place',place) ORDER BY entered_at, id),'[]')
        FROM (SELECT p.*, (SELECT min(t.time_limit) FROM workflow_transitions t
                WHERE (t.template_id,t.from_step)=(p.template_id,p.current_step)) AS time_limit
              FROM period_alerts p WHERE overdue ORDER BY entered_at, id LIMIT $11) o),
    'units', (SELECT coalesce(jsonb_agg(jsonb_build_object('code',code,'name',name,'path',path::text,
            'alerts',alerts,'active',active,'overdue',overdue,'pendingReports',pending_reports,
            'formsMissing',forms_missing) ORDER BY path),'[]') FROM unit_counts),
    'unitLevel', (SELECT l.labels->>'en' FROM unit_rows r JOIN hierarchy_levels l
            ON (l.hierarchy_id,l.depth)=(r.hierarchy_id,r.depth) LIMIT 1),
    'map', jsonb_build_object(
        'bounds', (SELECT jsonb_build_array(ST_XMin(box),ST_YMin(box),ST_XMax(box),ST_YMax(box)) FROM extent WHERE box IS NOT NULL),
        'outline', (SELECT ST_AsGeoJSON(ST_SimplifyPreserveTopology(geom,(SELECT t FROM tolerance)),5)::jsonb FROM outline),
        'units', (SELECT coalesce(jsonb_agg(jsonb_build_object('name',name,
            'geometry',ST_AsGeoJSON(ST_SimplifyPreserveTopology(geom,(SELECT t FROM tolerance)),5)::jsonb) ORDER BY path),'[]')
            FROM unit_rows WHERE geom IS NOT NULL AND EXISTS (SELECT FROM report_unit)),
        'forest', (SELECT coalesce(jsonb_agg(ST_AsGeoJSON(geom,5)::jsonb),'[]') FROM forest WHERE NOT ST_IsEmpty(geom)),
        'alerts', (SELECT coalesce(jsonb_agg(jsonb_build_array(round(ST_X(geom)::numeric,5),round(ST_Y(geom)::numeric,5),
            forest_class,NOT terminal,observed_at) ORDER BY observed_at, id),'[]') FROM map_points),
        'alertsShown', (SELECT count(*) FROM map_points),
        'incidents', (SELECT coalesce(jsonb_agg(jsonb_build_object('id',i.id::text,'active',NOT r.terminal,
            'bounds',jsonb_build_array(ST_XMin(b),ST_YMin(b),ST_XMax(b),ST_YMax(b)))),'[]')
            FROM (SELECT p.incident_id AS id, ST_Extent(p.geom)::geometry AS b FROM period_alerts p
                  WHERE p.incident_id IS NOT NULL AND p.id IN (SELECT id FROM map_points) GROUP BY p.incident_id) i
            JOIN incident_rows r ON r.id=i.id)))
