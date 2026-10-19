CREATE TYPE user_role AS ENUM ('USER', 'SELLER', 'ADMIN');

ALTER TABLE users DROP CONSTRAINT users_role_check;
ALTER TABLE users ALTER COLUMN role TYPE user_role USING role::user_role;
