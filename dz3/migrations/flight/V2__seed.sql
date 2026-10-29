INSERT INTO airlines (id, code, name)
VALUES ('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', 'SU', 'Aeroflot');

INSERT INTO flight_numbers (number, airline_id)
VALUES ('SU1234', 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa');

INSERT INTO flights (id, flight_number, origin, destination, departure_at, arrival_at, total_seats, available_seats, price_minor, status)
VALUES ('11111111-1111-4111-8111-111111111111', 'SU1234', 'SVO', 'LED', '2030-04-01T10:00:00Z', '2030-04-01T11:30:00Z', 10, 10, 125000, 'SCHEDULED');
