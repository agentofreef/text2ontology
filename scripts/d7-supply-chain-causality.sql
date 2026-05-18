-- D7 · Backfill ont_causality(join_key) for supply-chain links.
--
-- Prerequisite for the plan-mode composite Intent spec
-- (.omc/specs/plan-mode-composite-intent.md §5). The Lakehouse JOIN engine
-- walks ont_causality(relation_type='join_key'), not ont_link_type. Project
-- fdw-verify-045039 has 11 ont_link_type rows but only 4 sales-side links
-- have corresponding causality rows — the remaining 7 (cross-cutting +
-- supply chain) need this backfill so the hero question's `impact` step can
-- JOIN OrderLine → Order → Store and the recipe chain can walk
-- Ingredient → SKU → RecipeLine → MenuSpec → OrderLine.
--
-- Idempotent: each (from_prop_id, to_prop_id, join_key) tuple is created
-- only if no causality row already connects them.

\set ON_ERROR_STOP on

DO $$
DECLARE
    pid CONSTANT UUID := '57832811-fed2-482b-be41-9bf27e49ccf6';
    -- (from_od, from_col, to_od, to_col, cardinality)
    links CONSTANT TEXT[][] := ARRAY[
        ['MENUSPEC',  'menu_item_id', 'MENUITEM', 'id', 'N:1'],
        ['ORDERLINE', 'order_id',     'ORDER',    'id', 'N:1'],
        ['ORDERLINE', 'spec_id',      'MENUSPEC', 'id', 'N:1'],
        ['RECIPELINE','sku_code',     'SKU',      'id', 'N:1'],
        ['RECIPELINE','spec_id',      'MENUSPEC', 'id', 'N:1'],
        ['SKU',       'ingredient_id','INGREDIENT','id','N:1'],
        ['SKU',       'supplier_id',  'SUPPLIER', 'id', 'N:1']
    ];
    r          TEXT[];
    from_prop  UUID;
    to_prop    UUID;
    from_kid   UUID;
    to_kid     UUID;
    inserted   INT := 0;
    skipped    INT := 0;
BEGIN
    FOREACH r SLICE 1 IN ARRAY links LOOP
        -- Resolve property IDs.
        SELECT p.id INTO from_prop
        FROM ont_property p
        JOIN ont_object_type o ON o.id = p.object_type_id
        WHERE p.project_id = pid AND o.name = r[1] AND p.name = r[2];

        SELECT p.id INTO to_prop
        FROM ont_property p
        JOIN ont_object_type o ON o.id = p.object_type_id
        WHERE p.project_id = pid AND o.name = r[3] AND p.name = r[4];

        IF from_prop IS NULL OR to_prop IS NULL THEN
            RAISE WARNING 'skip %.% -> %.%: property not found (from=% to=%)',
                r[1], r[2], r[3], r[4], from_prop, to_prop;
            skipped := skipped + 1;
            CONTINUE;
        END IF;

        -- Skip if a causality row already connects these two property knowledges.
        IF EXISTS (
            SELECT 1
            FROM ont_causality c
            JOIN ont_knowledge fk ON fk.id = c.from_knowledge_id
                                  AND fk.anchor_type = 'property'
                                  AND fk.anchor_id::text = from_prop::text
            JOIN ont_knowledge tk ON tk.id = c.to_knowledge_id
                                  AND tk.anchor_type = 'property'
                                  AND tk.anchor_id::text = to_prop::text
            WHERE c.project_id = pid AND c.relation_type = 'join_key'
        ) THEN
            skipped := skipped + 1;
            CONTINUE;
        END IF;

        -- FROM-side knowledge (reuse if title already exists).
        SELECT id INTO from_kid
        FROM ont_knowledge
        WHERE project_id = pid AND anchor_type = 'property'
          AND anchor_id::text = from_prop::text
          AND title = r[1] || '.' || r[2] || ' (join_key)';

        IF from_kid IS NULL THEN
            INSERT INTO ont_knowledge (project_id, title, entry_type, anchor_type, anchor_id)
            VALUES (pid, r[1] || '.' || r[2] || ' (join_key)', 'concept', 'property', from_prop)
            RETURNING id INTO from_kid;
        END IF;

        -- TO-side knowledge (reuse if title already exists — STORE.id etc. is shared by multiple FKs).
        SELECT id INTO to_kid
        FROM ont_knowledge
        WHERE project_id = pid AND anchor_type = 'property'
          AND anchor_id::text = to_prop::text
          AND title = r[3] || '.' || r[4] || ' (join_key)';

        IF to_kid IS NULL THEN
            INSERT INTO ont_knowledge (project_id, title, entry_type, anchor_type, anchor_id)
            VALUES (pid, r[3] || '.' || r[4] || ' (join_key)', 'concept', 'property', to_prop)
            RETURNING id INTO to_kid;
        END IF;

        INSERT INTO ont_causality
            (project_id, from_knowledge_id, to_knowledge_id, relation_type, direction)
        VALUES (pid, from_kid, to_kid, 'join_key', r[5]);

        inserted := inserted + 1;
    END LOOP;

    RAISE NOTICE 'D7: inserted=% skipped=% (total links=%)', inserted, skipped, array_length(links, 1);
END $$;

-- Verify: total join_key edges for this project should be 11 (4 pre-existing + 7 new).
SELECT count(*) AS total_join_key_edges
FROM ont_causality
WHERE project_id = '57832811-fed2-482b-be41-9bf27e49ccf6'
  AND relation_type = 'join_key';
