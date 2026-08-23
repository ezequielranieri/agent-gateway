-- 0013_seed.sql
-- Seed data for default roles and SuperAdmin bootstrap helper

-- Insert default roles (global catalog)
INSERT INTO public.roles (id, name, description) VALUES
    ('00000000-0000-0000-0000-000000000001', 'admin', 'Full administrative access within tenant'),
    ('00000000-0000-0000-0000-000000000002', 'operator', 'Standard operational access within tenant'),
    ('00000000-0000-0000-0000-000000000003', 'viewer', 'Read-only access within tenant')
ON CONFLICT (name) DO NOTHING;

-- Helper function for SuperAdmin bootstrap (called by CLI)
-- This function creates the initial SuperAdmin and can be called from bootstrap CLI
-- Usage: SELECT * FROM bootstrap_super_admin('admin@example.com', '$argon2id$v=19$m=65536,t=1,p=4$...');
-- Returns the created super_admin.id or NULL if already exists

CREATE OR REPLACE FUNCTION bootstrap_super_admin(
    p_email citext,
    p_password_hash text
) RETURNS uuid AS $$
DECLARE
    v_id uuid;
BEGIN
    -- Check if SuperAdmin already exists
    SELECT id INTO v_id FROM public.super_admins WHERE email = p_email;
    IF v_id IS NOT NULL THEN
        RETURN NULL; -- Already exists
    END IF;

    -- Create new SuperAdmin
    INSERT INTO public.super_admins (email, password_hash)
    VALUES (p_email, p_password_hash)
    RETURNING id INTO v_id;

    RETURN v_id;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER;

COMMENT ON FUNCTION bootstrap_super_admin(citext, text) IS 'Bootstrap helper: creates first Super Admin. Returns NULL if email already exists.';