ALTER TABLE links ALTER COLUMN created_at SET DEFAULT NOW();
ALTER TABLE profiles ALTER COLUMN created_at SET DEFAULT NOW();
ALTER TABLE tokens ALTER COLUMN created_at SET DEFAULT NOW();