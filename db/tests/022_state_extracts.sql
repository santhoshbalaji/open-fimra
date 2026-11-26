BEGIN;
DO $$
DECLARE
    w uuid;
    window_id bigint;
    original uuid;
    correction uuid;
BEGIN
    INSERT INTO workspaces(slug,name) VALUES('extract-schema-test','Fictional state') RETURNING id INTO w;
    INSERT INTO state_extract_authorities VALUES(w,'XS-DB','fictional-units');
    INSERT INTO state_extract_windows(workspace_id,days) VALUES(w,'[2026-01-01,2026-01-02)') RETURNING id INTO window_id;
    BEGIN
        INSERT INTO state_extract_windows(workspace_id,days) VALUES(w,'[2026-01-01,2026-01-03)');
        RAISE EXCEPTION 'An overlapping window was accepted';
    EXCEPTION WHEN exclusion_violation THEN NULL;
    END;
    INSERT INTO exports(workspace_id,requested_by,kind,format,filters,requester_roles,row_count,expires_at,state_window,state_revision)
    VALUES(w,'schema-test','state_extract','json','{}','[]',2,'infinity',window_id,1) RETURNING id INTO original;
    BEGIN
        UPDATE exports SET status='ready',finished_at=now(),state_receipt='[]' WHERE id=original;
        RAISE EXCEPTION 'An extract without documents was accepted';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
    INSERT INTO export_rows(export_id,ordinal,workspace_id,state_document)
    VALUES(original,1,w,'incident'),(original,2,w,'preparedness');
    UPDATE exports SET status='ready',finished_at=now(),state_receipt='[]' WHERE id=original;
    BEGIN
        DELETE FROM export_rows WHERE export_id=original;
        RAISE EXCEPTION 'An extract row could be deleted';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
    BEGIN
        UPDATE exports SET state_receipt='{}' WHERE id=original;
        RAISE EXCEPTION 'A receipt could be changed';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
    BEGIN
        INSERT INTO exports(workspace_id,requested_by,kind,format,filters,requester_roles,row_count,expires_at,state_window,state_revision,state_supersedes)
        VALUES(w,'schema-test','state_extract','json','{}','[]',2,'infinity',window_id,3,original);
        RAISE EXCEPTION 'A revision skipped its parent';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
    INSERT INTO exports(workspace_id,requested_by,kind,format,filters,requester_roles,row_count,expires_at,state_window,state_revision,state_supersedes)
    VALUES(w,'schema-test','state_extract','json','{}','[]',2,'infinity',window_id,2,original) RETURNING id INTO correction;
    INSERT INTO export_rows(export_id,ordinal,workspace_id,state_document)
    VALUES(correction,1,w,'corrected incident'),(correction,2,w,'corrected preparedness');
    UPDATE exports SET status='ready',finished_at=now(),state_receipt='[]' WHERE id=correction;
    SET CONSTRAINTS state_extract_must_finish IMMEDIATE;
END;
$$;
ROLLBACK;
