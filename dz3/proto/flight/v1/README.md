# FlightService v1

[Контракт](flight.proto) использует UUID-строки, `google.protobuf.Timestamp` для
моментов времени и enum для статусов. `price_minor` — целое количество копеек RUB.
Дата поиска — необязательная строка `YYYY-MM-DD`, трактуется в UTC.
Все вызовы требуют metadata `x-api-key` со значением `FLIGHT_API_KEY`.
Отсутствующий или неверный ключ дает `UNAUTHENTICATED` до бизнес-логики.

| RPC | Результат | Бизнес-ошибки |
|---|---|---|
| SearchFlights | SCHEDULED-рейсы по origin/destination, необязательной UTC-дате | INVALID_ARGUMENT: неверный IATA-код, одинаковые аэропорты, неверная дата |
| GetFlight | Полная информация о рейсе | INVALID_ARGUMENT: неверный UUID; NOT_FOUND: нет рейса |
| ReserveSeats | ACTIVE-резерв для booking_id | INVALID_ARGUMENT: неверные UUID или число мест; NOT_FOUND: нет рейса; RESOURCE_EXHAUSTED: мало мест; FAILED_PRECONDITION: рейс не SCHEDULED или резерв уже закрыт; ALREADY_EXISTS: booking_id использован с другими параметрами |
| ReleaseReservation | released=true, если места возвращены этим вызовом; false при повторе или отсутствии резерва | INVALID_ARGUMENT: неверный booking_id |
| UpdateFlight | Рейс с новыми ценой и статусом | INVALID_ARGUMENT: неверный UUID, неположительная цена, неизвестный статус; NOT_FOUND: нет рейса |

Временные ошибки связи/БД могут давать `UNAVAILABLE`, истечение deadline —
`DEADLINE_EXCEEDED`. Непредвиденная внутренняя ошибка дает `INTERNAL`.
Booking повторяет только первые два кода, максимум три попытки.

`ReserveSeats` при повторе с теми же параметрами возвращает прежний ACTIVE-резерв.
После Release вернуть тот же `booking_id` в ACTIVE нельзя. Release без найденного
резерва сохраняет запрет на будущий Reserve с тем же ID: это закрывает гонку между
компенсацией и запоздалым запросом. Возврат мест и смена статуса атомарны.

`UpdateFlight` предназначен для управления ценой/статусом через сервисный API и
проверки инвалидации кеша. Уже сохраненные суммы бронирований не пересчитываются.

Пример с `grpcurl` из папки `dz3`:

```sh
grpcurl -plaintext -import-path proto -proto flight/v1/flight.proto \
  -H 'x-api-key: homework-flight-service-key-change-me' \
  -d '{"id":"11111111-1111-4111-8111-111111111111"}' \
  localhost:50061 flight.v1.FlightService/GetFlight
```

HTTP отображает INVALID_ARGUMENT в 400, NOT_FOUND в 404,
ALREADY_EXISTS/FAILED_PRECONDITION/RESOURCE_EXHAUSTED/ABORTED в 409,
UNAVAILABLE/DEADLINE_EXCEEDED в 503. `CIRCUIT_OPEN` и `OPERATION_PENDING` также дают
503; последний включает `booking_id`. Формат ошибок задан в OpenAPI.
