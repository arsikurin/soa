CREATE TYPE order_status AS ENUM ('CREATED', 'PAYMENT_PENDING', 'PAID', 'SHIPPED', 'COMPLETED', 'CANCELED');
CREATE TYPE discount_type AS ENUM ('PERCENTAGE', 'FIXED_AMOUNT');
CREATE TYPE user_operation_type AS ENUM ('CREATE_ORDER', 'UPDATE_ORDER');

CREATE TABLE promo_codes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code VARCHAR(20) NOT NULL UNIQUE CHECK (code ~ '^[A-Z0-9_]{4,20}$'),
    discount_type discount_type NOT NULL,
    discount_value NUMERIC(12, 2) NOT NULL CHECK (discount_value >= 0.01),
    min_order_amount NUMERIC(12, 2) NOT NULL CHECK (min_order_amount >= 0),
    max_uses INTEGER NOT NULL CHECK (max_uses >= 1),
    current_uses INTEGER NOT NULL DEFAULT 0,
    valid_from TIMESTAMPTZ NOT NULL,
    valid_until TIMESTAMPTZ NOT NULL,
    active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT promo_codes_uses_check CHECK (current_uses BETWEEN 0 AND max_uses),
    CONSTRAINT promo_codes_interval_check CHECK (valid_until > valid_from)
);

CREATE TABLE orders (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL,
    status order_status NOT NULL,
    total_amount NUMERIC(12, 2) NOT NULL CHECK (total_amount >= 0),
    discount_amount NUMERIC(12, 2) NOT NULL DEFAULT 0 CHECK (discount_amount >= 0),
    promo_code_id UUID REFERENCES promo_codes(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX orders_one_active_per_user_idx ON orders(user_id)
    WHERE status IN ('CREATED', 'PAYMENT_PENDING');
CREATE INDEX orders_user_idx ON orders(user_id, created_at DESC);
CREATE INDEX orders_promo_idx ON orders(promo_code_id) WHERE promo_code_id IS NOT NULL;

CREATE TABLE order_items (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id UUID NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    product_id UUID NOT NULL,
    quantity INTEGER NOT NULL CHECK (quantity BETWEEN 1 AND 999),
    price_at_order NUMERIC(12, 2) NOT NULL CHECK (price_at_order >= 0.01),
    CONSTRAINT order_items_product_unique UNIQUE (order_id, product_id)
);
CREATE INDEX order_items_product_idx ON order_items(product_id);

CREATE TABLE user_operations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL,
    operation_type user_operation_type NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX user_operations_rate_limit_idx ON user_operations(user_id, operation_type, created_at DESC);

CREATE FUNCTION set_updated_at() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at = clock_timestamp();
    RETURN NEW;
END;
$$;

CREATE TRIGGER promo_codes_updated_at BEFORE UPDATE ON promo_codes FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER orders_updated_at BEFORE UPDATE ON orders FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TABLE actor_locks (id UUID PRIMARY KEY);
CREATE TABLE transaction_journal (
    id UUID PRIMARY KEY,
    order_id UUID NOT NULL,
    decision TEXT NOT NULL DEFAULT 'PREPARING' CHECK (decision IN ('PREPARING','COMMIT','ABORT')),
    completed BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX transaction_recovery_idx ON transaction_journal(created_at) WHERE NOT completed;
CREATE TABLE transaction_receipts (id UUID PRIMARY KEY);
CREATE FUNCTION protect_transaction_decision() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.decision <> 'PREPARING' AND NEW.decision <> OLD.decision THEN
        RAISE EXCEPTION 'transaction decision is immutable';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER transaction_decision_immutable BEFORE UPDATE ON transaction_journal
    FOR EACH ROW EXECUTE FUNCTION protect_transaction_decision();
