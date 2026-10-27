-- Only the isolated restore role sleeps. Source creation/export remains fast.
CREATE FUNCTION public.synthetic_restore_delay() RETURNS boolean LANGUAGE plpgsql AS $$
BEGIN
  IF current_user = 'rehearsal_owner' THEN
    PERFORM pg_sleep(60);
  END IF;
  RETURN true;
END;
$$;
SET ROLE fixture_owner;
ALTER TABLE public.archive_probe ADD CONSTRAINT synthetic_restore_budget
  CHECK (public.synthetic_restore_delay());
RESET ROLE;
