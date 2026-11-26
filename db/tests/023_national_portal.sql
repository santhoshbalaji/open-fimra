BEGIN;
DO $$
DECLARE
    hub uuid;
    h uuid;
    state bigint;
    reporting_window bigint;
    original uuid := gen_random_uuid();
    correction uuid := gen_random_uuid();
    payload jsonb;
    receipt jsonb;
BEGIN
    INSERT INTO workspaces(slug,name,kind) VALUES ('national-schema-test','Fictional national workspace','national') RETURNING id INTO hub;
    INSERT INTO hierarchies(workspace_id,slug,name,kind) VALUES (hub,'exchange','Exchange','other') RETURNING id INTO h;
    INSERT INTO hierarchy_levels(hierarchy_id,depth,slug,labels) VALUES (h,1,'state','{"en":"State"}');
    INSERT INTO units(hierarchy_id,depth,code,name,path) VALUES (h,1,'XS-DB','Fictional state','hub.db');
    INSERT INTO national_states(workspace_id,hierarchy_id,unit_path,state_code,unit_code_system)
    VALUES (hub,h,'hub.db','XS-DB','fictional-units') RETURNING id INTO state;
    BEGIN
        INSERT INTO national_state_keys VALUES (state,repeat('a',32),decode(repeat('00',32),'hex'));
        RAISE EXCEPTION 'A public key with the wrong fingerprint was accepted';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
    INSERT INTO national_windows(state_id,days) VALUES (state,'[2026-01-01,2026-01-02)') RETURNING id INTO reporting_window;
    BEGIN
        INSERT INTO national_windows(state_id,days) VALUES (state,'[2026-01-01,2026-01-03)');
        RAISE EXCEPTION 'An overlapping reporting window was accepted';
    EXCEPTION WHEN exclusion_violation THEN NULL;
    END;
    BEGIN
        UPDATE national_states SET state_code='XS-OTHER' WHERE id=state;
        RAISE EXCEPTION 'A reporting authority with records was reassigned';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
    BEGIN
        UPDATE national_windows SET days='[2026-01-02,2026-01-03)' WHERE id=reporting_window;
        RAISE EXCEPTION 'A reporting window was moved';
    EXCEPTION WHEN insufficient_privilege THEN NULL;
    END;
    payload := jsonb_build_object('id',original,'kind','incident','revision',1,
        'coverage',jsonb_build_object('status','zero'),
        'totals',jsonb_build_object('incidents',jsonb_build_object('new',0)));
    receipt := jsonb_build_object('extractId',original,'kind','incident','revision',1,'sha256',repeat('1',64));
    INSERT INTO national_extracts (window_id,state_id,kind,id,revision,signed_document,payload,receipt)
    VALUES (reporting_window,state,'incident',original,1,'fixture',payload,receipt);
    INSERT INTO national_publications(window_id,extract_id) VALUES (reporting_window,original);
    IF NOT EXISTS (SELECT FROM national_public_aggregates a WHERE a.window_id=reporting_window
                   AND a.aggregate->'confirmedFires' = 'null'::jsonb AND a.aggregate->>'coverage' = 'reported') THEN
        RAISE EXCEPTION 'A small public count was exposed';
    END IF;
    payload := jsonb_build_object('id',correction,'kind','incident','revision',2,'supersedes',original);
    receipt := jsonb_build_object('extractId',correction,'kind','incident','revision',2,'sha256',repeat('2',64));
    BEGIN
        INSERT INTO national_extracts (window_id,state_id,kind,id,revision,supersedes,signed_document,payload,receipt)
        VALUES (reporting_window,state,'incident',correction,2,correction,'fixture',payload || jsonb_build_object('supersedes',correction),receipt);
        RAISE EXCEPTION 'A correction without its predecessor was accepted';
    EXCEPTION WHEN foreign_key_violation THEN NULL;
    END;
    INSERT INTO national_extracts (window_id,state_id,kind,id,revision,supersedes,signed_document,payload,receipt)
    VALUES (reporting_window,state,'incident',correction,2,original,'fixture',payload,receipt);
    IF EXISTS (SELECT FROM national_public_aggregates a WHERE a.window_id=reporting_window)
        OR (SELECT array_agg(x.id) FROM national_current_extracts x WHERE x.window_id=reporting_window) <> ARRAY[correction] THEN
        RAISE EXCEPTION 'A correction left the old version current or publicly visible';
    END IF;
    BEGIN
        UPDATE national_extracts SET signed_document='changed' WHERE id=original;
        RAISE EXCEPTION 'A receipt could be changed';
    EXCEPTION WHEN insufficient_privilege THEN NULL;
    END;
    BEGIN
        INSERT INTO national_incident_updates (
            state_id,incident_ref,shared_event_id,status,severity,occurred_at,precision_decimals,
            longitude,latitude,threats,aid_requested,aid_accepted,provisional_impacts
        ) VALUES (state,'TEST-1',gen_random_uuid(),'active','major',now(),0,1.11,0,'','','','');
        RAISE EXCEPTION 'The incident channel stored more than the declared precision';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
END;
$$;
SET CONSTRAINTS ALL IMMEDIATE;
ROLLBACK;
