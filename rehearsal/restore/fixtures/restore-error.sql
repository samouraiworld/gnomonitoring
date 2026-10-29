-- Creating an internal-language function requires superuser. The isolated
-- restore role must not acquire that privilege to make this archive succeed.
CREATE FUNCTION public.synthetic_restore_requires_admin(integer, integer)
RETURNS boolean LANGUAGE internal IMMUTABLE STRICT AS 'int4eq';
