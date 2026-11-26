WITH intake AS (
    SELECT f.*, a.forest_class, a.dispatch_lead, m.incident_id
    FROM fire_intake f JOIN alerts a ON a.id=f.alert_id
    JOIN incident_members m ON m.alert_id=a.id
    WHERE f.workspace_id=$1
), ridge AS (
    SELECT * FROM intake WHERE note='Ridge fire reported by radio from West Lookout.'
), case_alerts AS (
    SELECT a.*, d.source, d.instrument, d.raw->>'externalAlertRef' AS ref
    FROM ridge r JOIN incident_members m USING (incident_id) JOIN alerts a ON a.id=m.alert_id
    JOIN detections d ON (d.id,d.observed_at)=(a.detection_id,a.observed_at)
), reserve AS (
    SELECT a.*, d.raw->>'externalAlertRef' AS ref
    FROM alerts a JOIN detections d ON (d.id,d.observed_at)=(a.detection_id,a.observed_at)
    WHERE a.workspace_id=$1 AND d.raw->>'externalAlertRef' IN ('RESERVE-OLD','RESERVE-NEW')
), checks(name, passed) AS (
    VALUES
    ('duty handover', EXISTS (
        SELECT FROM duty_shifts outgoing JOIN duty_shifts incoming
          ON incoming.workspace_id=outgoing.workspace_id AND incoming.starts_at=outgoing.ends_at
        JOIN duty_members m ON m.shift_id=incoming.id JOIN users u ON u.id=m.user_id
        JOIN step_log first ON first.user_id=(SELECT user_id FROM duty_members WHERE shift_id=outgoing.id)
        JOIN step_log next ON next.user_id=m.user_id AND next.incident_id=first.incident_id
        JOIN ridge r ON r.incident_id=first.incident_id
        WHERE outgoing.workspace_id=$1 AND outgoing.name='West control room outgoing watch'
          AND u.username='duty.officer' AND first.to_step='acknowledged' AND first.actual_at<incoming.starts_at
          AND next.to_step='assigned' AND next.actual_at>=incoming.starts_at
    )),
    ('cross-division aid', EXISTS (
        SELECT FROM ridge r JOIN alert_support_requests s ON s.alert_id=r.alert_id
        JOIN users u ON u.id=s.requested_by
        WHERE s.target->>'path'='ds.c1.d2' AND u.username='duty.officer'
    )),
    ('radio before satellite and public intake', EXISTS (
        SELECT FROM ridge r JOIN intake caller USING(incident_id)
        WHERE r.source='radio' AND caller.source='public'
          AND r.observed_at < (SELECT min(a.observed_at) FROM case_alerts a WHERE a.source <> 'manual')
    )),
    ('two VIIRS passes and a source feedback receipt', (
        SELECT count(*) FROM case_alerts WHERE instrument='VIIRS' AND ref IN ('RIDGE-VIIRS-1','RIDGE-VIIRS-2')
    )=2 AND EXISTS (
        SELECT FROM case_alerts a JOIN source_feedback f ON f.alert_id=a.id
        WHERE a.source='demo_fsi' AND f.external_alert_ref='DEMO-FSI-RIDGE-37' AND f.status='accepted'
    )),
    ('large fire with observations in both response divisions and a flash update', EXISTS (
        SELECT FROM ridge r JOIN step_log s ON s.incident_id=r.incident_id
        WHERE s.to_step='flash_update' AND s.response_note->>'status' LIKE 'Large surface fire%'
    ) AND (
        SELECT count(DISTINCT subpath((original_dispatch->>'unitPath')::ltree,0,3))
        FROM case_alerts WHERE ref IN ('RIDGE-VIIRS-1','RIDGE-VIIRS-2')
    )=2 AND (
        SELECT count(DISTINCT overlay->>'kind')
        FROM case_alerts a CROSS JOIN LATERAL jsonb_array_elements(a.boundary_snapshot->'overlays') overlay
        WHERE a.ref='RIDGE-VIIRS-1' AND overlay->>'kind' IN ('civil_district','community')
    )=2),
    ('no fire found', EXISTS (
        SELECT FROM intake f JOIN workflow_instances wi ON wi.incident_id=f.incident_id
        JOIN step_log s ON s.incident_id=f.incident_id
        JOIN users u ON u.id=s.user_id
        WHERE f.note='Smoke reported near Reed Section.' AND wi.current_step='false_alarm'
          AND s.to_step='reached_site' AND s.location IS NOT NULL AND u.username='section.forester'
    )),
    ('authorised prescribed burn', EXISTS (
        SELECT FROM intake f JOIN workflow_instances wi ON wi.incident_id=f.incident_id
        JOIN prescribed_burn_alerts b ON b.alert_id=f.alert_id
        JOIN submissions s ON s.operations_record_id=b.record_id JOIN approvals v ON v.submission_id=s.id
        WHERE wi.current_step='controlled_burn' AND v.decision='approved'
    )),
    ('out-of-jurisdiction heat source', EXISTS (
        SELECT FROM intake f JOIN workflow_instances wi ON wi.incident_id=f.incident_id
        JOIN alerts a ON a.id=f.alert_id JOIN detections d ON (d.id,d.observed_at)=(a.detection_id,a.observed_at)
        JOIN workspaces w ON w.id=a.workspace_id
        WHERE f.note LIKE 'Work barge heat%' AND wi.current_step='outside_jurisdiction' AND NOT ST_Covers(w.boundary,d.geom)
    )),
    ('inaccessible point', EXISTS (
        SELECT FROM intake f JOIN workflow_instances wi ON wi.incident_id=f.incident_id
        WHERE f.note LIKE 'The washed-out bridge%' AND wi.current_step='not_reachable'
    )),
    ('late boundary transfer with original lead and different observation versions', EXISTS (
        SELECT FROM reserve earlier CROSS JOIN reserve later
        CROSS JOIN LATERAL jsonb_array_elements(earlier.boundary_snapshot->'overlays') o1
        CROSS JOIN LATERAL jsonb_array_elements(later.boundary_snapshot->'overlays') o2
        WHERE earlier.ref='RESERVE-OLD' AND later.ref='RESERVE-NEW'
          AND earlier.observed_at < later.observed_at AND earlier.id > later.id
          AND earlier.original_dispatch #>> '{lead,path}'='ds.c2.d4'
          AND earlier.dispatch_lead->>'path'='dr.w1' AND earlier.assignment->>'action'='transfer'
          AND later.original_dispatch #>> '{lead,path}'='dr.w1'
          AND o1->>'zone'='core' AND o2->>'zone'='core' AND o1->>'versionId'<>o2->>'versionId'
    )),
    ('late phone GPS and one uploaded photo', (
        SELECT count(*) FROM ridge r JOIN submissions s ON s.alert_id=r.alert_id
        JOIN submission_attachments sa ON sa.submission_id=s.id JOIN attachments a ON a.id=sa.attachment_id
        JOIN users u ON u.id=s.user_id
        WHERE s.received_at > s.device_time AND s.device_time >= r.observed_at
          AND sa.field='site_photo' AND a.content_type='image/png' AND a.uploaded_by=s.user_id
          AND u.username='beat.alternate' AND ST_X(s.location)=86.311 AND ST_Y(s.location)=16.4191
    )=1),
    ('returned estimate and approved linked revision', EXISTS (
        SELECT FROM ridge r JOIN submissions p ON p.alert_id=r.alert_id JOIN approvals a ON a.submission_id=p.id
        JOIN submissions n ON n.provisional_submission_id=p.id JOIN approvals b ON b.submission_id=n.id
        WHERE a.decision='returned' AND a.note<>'' AND b.decision='approved'
          AND (p.answers->>'area_ha')::numeric=84 AND (n.answers->>'area_ha')::numeric=62
    )),
    ('restricted referral linked to the ridge case', EXISTS (
        SELECT FROM ridge r JOIN operations_records o USING (incident_id)
        JOIN submissions s ON s.operations_record_id=o.id JOIN approvals a ON a.submission_id=s.id
        JOIN referral_grants g ON g.workspace_id=o.workspace_id AND g.unit_path=o.unit_path AND g.kind=o.kind
        JOIN users u ON u.id=g.user_id
        WHERE o.kind='offence' AND a.decision='approved' AND u.username='division.officer'
    )),
    ('approved nil return and an expected missing return', EXISTS (
        SELECT FROM situation_reports r JOIN daily_return_decisions d ON d.report_id=r.id
        WHERE r.workspace_id=$1 AND r.period_from=$2::date AND r.unit_path='ds.c2.d3'
          AND d.decision='approved' AND r.content #>> '{dailyReturn,nil}'='true'
    ) AND EXISTS (
        SELECT FROM daily_return_policies p CROSS JOIN LATERAL daily_return_units(
            p.hierarchy_id,p.depth,daily_return_cutoff($2::date,p.cutoff,p.time_zone)
        ) u
        WHERE p.workspace_id=$1 AND p.season @> $2::date AND u.path='ds.c2.d4'
          AND NOT EXISTS (SELECT FROM situation_reports r WHERE r.daily_policy_id=p.id
                          AND r.unit_path=u.path AND r.period_from=$2::date)
    )),
    ('approved aggregate-only export', EXISTS (
        SELECT FROM exports e JOIN export_rows r ON r.export_id=e.id
        WHERE e.workspace_id=$1 AND e.kind='state_extract' AND e.status='ready'
          AND jsonb_array_length(e.state_receipt)=2 AND r.ordinal=1 AND r.detection_id IS NULL
          AND convert_from(r.state_document,'UTF8')::jsonb #>> '{payload,source}'='approved_daily_returns_and_assessments'
          AND NOT (convert_from(r.state_document,'UTF8') LIKE ANY(ARRAY['%DEMO-RIDGE-REGISTER%','%demo-ridge.png%','%86.311%','%West Lookout%']))
    ))
)
SELECT coalesce(array_agg(name ORDER BY name) FILTER (WHERE passed IS DISTINCT FROM true),'{}') FROM checks;
