CREATE TABLE bookings (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL,
    flight_id UUID NOT NULL,
    passenger_name TEXT NOT NULL CHECK (length(btrim(passenger_name)) > 0 AND length(passenger_name) <= 255),
    passenger_email TEXT NOT NULL CHECK (length(passenger_email) BETWEEN 3 AND 254),
    seat_count INTEGER NOT NULL CHECK (seat_count BETWEEN 1 AND 999),
    total_price_minor BIGINT NOT NULL CHECK (total_price_minor > 0),
    status TEXT NOT NULL CHECK (status IN ('CONFIRMED', 'CANCELLED')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX bookings_user_created_idx ON bookings (user_id, created_at DESC, id);

CREATE TABLE booking_operations (
    booking_id UUID PRIMARY KEY,
    user_id UUID NOT NULL,
    flight_id UUID NOT NULL,
    passenger_name TEXT NOT NULL,
    passenger_email TEXT NOT NULL,
    seat_count INTEGER NOT NULL CHECK (seat_count BETWEEN 1 AND 999),
    kind TEXT NOT NULL CHECK (kind IN ('CREATE', 'CANCEL')),
    state TEXT NOT NULL CHECK (state IN ('PENDING', 'DONE', 'FAILED')),
    failure_code INTEGER NOT NULL DEFAULT 10,
    failure_message TEXT NOT NULL DEFAULT 'booking creation was interrupted',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX booking_operations_pending_idx ON booking_operations (updated_at, booking_id) WHERE state = 'PENDING';
