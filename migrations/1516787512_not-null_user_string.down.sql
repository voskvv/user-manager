ALTER TABLE users
  ALTER COLUMN role DROP NOT NULL,
  ALTER COLUMN is_active DROP NOT NULL,
  ALTER COLUMN is_deleted DROP NOT NULL,
  ALTER COLUMN is_in_blacklist DROP NOT NULL;