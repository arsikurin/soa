CREATE TYPE product_status AS ENUM ('ACTIVE', 'INACTIVE', 'ARCHIVED');

ALTER TABLE products DROP CONSTRAINT products_status_check;
ALTER TABLE products ALTER COLUMN status TYPE product_status USING status::product_status;
