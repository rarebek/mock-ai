-- Learning centres as tenants inside one platform.
--
-- Content belongs to a centre (centre_id set) or to the global library
-- (centre_id NULL). Students belong to a centre or are global. Every existing
-- row becomes global, so this migration changes nothing that is visible today.
--
-- This is orthogonal to access_scope: a centre's exam is public *within that
-- centre*, while access_scope='private' stays one student's own upload.
CREATE TABLE centres (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug text NOT NULL UNIQUE CHECK (slug = lower(slug) AND slug <> ''),
    name text NOT NULL CHECK (name <> ''),
    -- Students join by opening t.me/<bot>?start=c_<join_code>, so this travels
    -- in a URL and in chat messages: it must be unguessable, not a counter.
    join_code text NOT NULL UNIQUE CHECK (length(join_code) >= 16),
    -- Whether this centre's students also see the global library. On by
    -- default so a new centre's students open a full app rather than an empty
    -- one; a centre running a closed curriculum turns it off.
    include_global boolean NOT NULL DEFAULT true,
    disabled_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- A student with no centre is a direct registrant and sees the global library.
ALTER TABLE users ADD COLUMN centre_id uuid REFERENCES centres(id) ON DELETE SET NULL;
CREATE INDEX users_centre_idx ON users (centre_id) WHERE centre_id IS NOT NULL;

-- Content ownership. RESTRICT rather than CASCADE or SET NULL: deleting a
-- centre must not silently destroy its exams, and must never promote them into
-- the global library where every other centre would suddenly see them.
ALTER TABLE exams ADD COLUMN centre_id uuid REFERENCES centres(id) ON DELETE RESTRICT;
ALTER TABLE managed_content ADD COLUMN centre_id uuid REFERENCES centres(id) ON DELETE RESTRICT;
ALTER TABLE content_drafts ADD COLUMN centre_id uuid REFERENCES centres(id) ON DELETE RESTRICT;
ALTER TABLE content_imports ADD COLUMN centre_id uuid REFERENCES centres(id) ON DELETE RESTRICT;
CREATE INDEX exams_centre_idx ON exams (centre_id) WHERE centre_id IS NOT NULL;
CREATE INDEX managed_content_centre_idx ON managed_content (centre_id) WHERE centre_id IS NOT NULL;

-- A centre admin administers one centre. NULL is a platform administrator, who
-- owns the global library and creates centres.
ALTER TABLE admins ADD COLUMN centre_id uuid REFERENCES centres(id) ON DELETE CASCADE;
CREATE INDEX admins_centre_idx ON admins (centre_id) WHERE centre_id IS NOT NULL;
