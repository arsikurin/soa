CREATE TABLE products (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(255) NOT NULL CHECK (char_length(name) BETWEEN 1 AND 255),
    description VARCHAR(4000),
    price NUMERIC(12, 2) NOT NULL CHECK (price >= 0.01),
    stock INTEGER NOT NULL CHECK (stock >= 0),
    category VARCHAR(100) NOT NULL CHECK (char_length(category) BETWEEN 1 AND 100),
    status TEXT NOT NULL CHECK (status IN ('ACTIVE', 'INACTIVE', 'ARCHIVED')),
    seller_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX products_status_idx ON products(status);
CREATE INDEX products_seller_idx ON products(seller_id);
CREATE INDEX products_catalog_idx ON products(created_at, id);

CREATE FUNCTION set_updated_at() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at = clock_timestamp();
    RETURN NEW;
END;
$$;

CREATE TRIGGER products_updated_at BEFORE UPDATE ON products FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE reservation_journal (
    id UUID PRIMARY KEY,
    digest TEXT,
    payload JSONB,
    result JSONB,
    state TEXT NOT NULL CHECK (state IN ('PREPARING','PREPARED','COMMIT','ABORT')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE reservation_receipts (id UUID PRIMARY KEY, result JSONB NOT NULL);
CREATE FUNCTION protect_reservation_decision() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.state IN ('COMMIT','ABORT') AND NEW.state <> OLD.state THEN
        RAISE EXCEPTION 'reservation decision is immutable';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER reservation_decision_immutable BEFORE UPDATE ON reservation_journal
    FOR EACH ROW EXECUTE FUNCTION protect_reservation_decision();
