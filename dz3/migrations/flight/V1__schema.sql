CREATE TABLE airlines (
    id uuid PRIMARY KEY,
    code text NOT NULL UNIQUE CHECK (code ~ '^[A-Z0-9]{2}$'),
    name text NOT NULL CHECK (length(trim(name)) > 0)
);

CREATE TABLE flight_numbers (
    number text PRIMARY KEY CHECK (number ~ '^[A-Z0-9]{2}[0-9]{1,4}$'),
    airline_id uuid NOT NULL REFERENCES airlines(id)
);

CREATE TABLE flights (
    id uuid PRIMARY KEY,
    flight_number text NOT NULL REFERENCES flight_numbers(number),
    origin text NOT NULL CHECK (origin ~ '^[A-Z]{3}$'),
    destination text NOT NULL CHECK (destination ~ '^[A-Z]{3}$'),
    departure_at timestamptz NOT NULL,
    arrival_at timestamptz NOT NULL,
    total_seats integer NOT NULL CHECK (total_seats > 0),
    available_seats integer NOT NULL CHECK (available_seats >= 0 AND available_seats <= total_seats),
    price_minor bigint NOT NULL CHECK (price_minor > 0),
    status text NOT NULL CHECK (status IN ('SCHEDULED', 'DEPARTED', 'CANCELLED', 'COMPLETED')),
    CHECK (origin <> destination),
    CHECK (arrival_at > departure_at)
);

CREATE UNIQUE INDEX flights_number_departure_day_idx ON flights (flight_number, ((departure_at AT TIME ZONE 'UTC')::date));
CREATE INDEX flights_search_idx ON flights (origin, destination, departure_at) WHERE status = 'SCHEDULED';

CREATE TABLE seat_reservations (
    id uuid PRIMARY KEY,
    booking_id uuid NOT NULL UNIQUE,
    flight_id uuid NOT NULL REFERENCES flights(id),
    seat_count integer NOT NULL CHECK (seat_count > 0),
    status text NOT NULL CHECK (status IN ('ACTIVE', 'RELEASED', 'EXPIRED')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX seat_reservations_flight_idx ON seat_reservations (flight_id);

CREATE TABLE reservation_fences (
    booking_id uuid PRIMARY KEY,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE cache_revision (
    id smallint PRIMARY KEY CHECK (id = 1),
    revision bigint NOT NULL CHECK (revision > 0)
);

INSERT INTO cache_revision (id, revision) VALUES (1, 1);
