DO $$
BEGIN
    IF to_regclass('public.complimentary_grants') IS NOT NULL THEN
        IF EXISTS (SELECT 1 FROM complimentary_grants) THEN
            RAISE EXCEPTION 'complimentary_grants must be empty before removal';
        END IF;
        DROP TABLE complimentary_grants;
    END IF;
END
$$;
