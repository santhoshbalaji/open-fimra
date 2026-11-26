WITH approved AS (
    SELECT DISTINCT ON (r.id) r.kind,s.completes_record,s.answers
    FROM operations_records r JOIN submissions s ON s.operations_record_id=r.id
    JOIN approvals a ON a.submission_id=s.id AND a.decision='approved'
    WHERE r.workspace_id=$1 AND r.kind IN ('preparedness','training','readiness')
      AND s.received_at >= $2 AND s.received_at < $3
    ORDER BY r.id,a.decided_at DESC,s.id DESC
), quantities AS (
    SELECT kind, completes_record,
        CASE WHEN kind='training' AND jsonb_typeof(answers->'participants')='number'
            THEN (answers->>'participants')::numeric END AS trained,
        CASE WHEN kind='preparedness' AND jsonb_typeof(answers->'target_quantity')='number'
            THEN (answers->>'target_quantity')::numeric END AS target,
        CASE WHEN kind='preparedness' AND jsonb_typeof(answers->'work_done_quantity')='number'
            THEN (answers->>'work_done_quantity')::numeric END AS done,
        CASE WHEN kind='preparedness' AND jsonb_typeof(answers->'expenditure_inr')='number'
            THEN (answers->>'expenditure_inr')::numeric END AS expenditure
    FROM approved
)
SELECT count(*),count(*) FILTER (WHERE kind='preparedness'),count(*) FILTER (WHERE kind='preparedness' AND completes_record),
    count(*) FILTER (WHERE kind='training'),count(*) FILTER (WHERE kind='readiness'),
    sum(trained) FILTER (WHERE trained >= 0 AND trained=trunc(trained)),
    count(trained) FILTER (WHERE trained >= 0 AND trained=trunc(trained)),
    sum(target) FILTER (WHERE target >= 0 AND target=trunc(target)),
    count(target) FILTER (WHERE target >= 0 AND target=trunc(target)),
    sum(done) FILTER (WHERE done >= 0 AND done=trunc(done)),
    count(done) FILTER (WHERE done >= 0 AND done=trunc(done)),
    sum(expenditure) FILTER (WHERE expenditure >= 0),count(expenditure) FILTER (WHERE expenditure >= 0)
FROM quantities
