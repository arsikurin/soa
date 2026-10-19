# Marketplace API

Go API товаров, заказов и промокодов. HTTP-сервер Gateway генерируется из OpenAPI
через ogen. Users, Catalog и Orders общаются по gRPC с mTLS и используют отдельные
PostgreSQL. Миграции применяет Flyway, заказ и резерв товара фиксируются через 2PC.

## Запуск

Команды ниже выполняются из папки `dz12`.

```sh
docker compose up --build -d --wait --wait-timeout 180
docker compose logs users-migrate catalog-migrate orders-migrate gateway
curl -i http://localhost:8080/health
```

API: `localhost:8080`. PostgreSQL: Users — `localhost:5433`, Catalog —
`localhost:5434`, Orders — `localhost:5435`. Имя базы, пользователь и пароль:
`users`, `catalog`, `orders` соответственно. Все опубликованные порты привязаны к loopback.
`docker compose down` останавливает систему, сохраняя данные в томах.

Настройки — в [.env.example](.env.example). При первом запуске создается администратор
`admin@example.com` с паролем `Homework-Admin-Password-2026!`.
Их меняют `BOOTSTRAP_ADMIN_EMAIL` и `BOOTSTRAP_ADMIN_PASSWORD` до первого запуска.
`JWT_SECRET` задает ключ подписи токенов. Access token действует 15 минут, refresh token —
7 дней; обновление погашает предыдущий refresh token.
`ORDER_RATE_LIMIT_MINUTES` задает интервал между успешными созданиями или обновлениями
заказа одного пользователя. По умолчанию — 1 минута; два типа операций считаются отдельно.

## API

[OpenAPI](api/openapi.yaml) содержит схемы, ограничения и каталог ошибок.
Все операции, кроме `/auth/*` и `/health`, требуют `Authorization: Bearer <access_token>`.

| Метод и путь | Назначение | Доступ |
|---|---|---|
| `POST /auth/register` | Регистрация | USER или SELLER |
| `POST /auth/login` | Access и refresh token | По email и паролю |
| `POST /auth/refresh` | Ротация токенов | По refresh token |
| `GET /products` | Каталог с пагинацией и фильтрами | Все роли |
| `GET /products/{id}` | Товар | Все роли |
| `POST /products` | Создание товара | SELLER, ADMIN |
| `PUT /products/{id}` | Замена данных товара | Владелец SELLER, ADMIN |
| `DELETE /products/{id}` | Перевод в ARCHIVED | Владелец SELLER, ADMIN |
| `POST /orders` | Создание и резервирование | USER, ADMIN |
| `GET /orders/{id}` | Заказ со снимками цен | Владелец USER, ADMIN |
| `PUT /orders/{id}` | Замена позиций заказа | Владелец USER, ADMIN |
| `POST /orders/{id}/cancel` | Отмена и возврат остатков | Владелец USER, ADMIN |
| `POST /orders/{id}/status` | Следующее состояние заказа | ADMIN |
| `POST /promo-codes` | Создание промокода | SELLER, ADMIN |

Регистрация продавца и вход:

```sh
curl -sS localhost:8080/auth/register -H 'Content-Type: application/json' \
  -d '{"email":"seller@example.com","password":"Seller-password-2026!","role":"SELLER"}'
curl -sS localhost:8080/auth/login -H 'Content-Type: application/json' \
  -d '{"email":"seller@example.com","password":"Seller-password-2026!"}'
```

Сохраните `access_token` в `TOKEN`, затем создайте товар и запросите каталог:

```sh
curl -sS localhost:8080/products \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"Клавиатура","description":null,"price":2500.00,"stock":10,"category":"electronics","status":"ACTIVE"}'
curl -sS 'localhost:8080/products?page=0&size=20&status=ACTIVE&category=electronics' \
  -H "Authorization: Bearer $TOKEN"
```

Список содержит `items`, `totalElements`, `page`, `size`. Страницы начинаются с 0,
размер по умолчанию — 20, максимум — 100. Категория сравнивается точно.
Без фильтра видны и архивные товары. `PUT /products/{id}` заменяет редактируемые поля;
отсутствие `description` означает `null`.

Заказ принимает `items: [{"product_id":"UUID","quantity":1}]` и необязательный
`promo_code`. У пользователя может быть один заказ в `CREATED` или `PAYMENT_PENDING`.
Позиции меняются только в `CREATED`, отмена доступна в обоих состояниях.

```text
CREATED → PAYMENT_PENDING → PAID → SHIPPED → COMPLETED
   └───────────┴────────────→ CANCELED
```

Цена сохраняется в позиции заказа; при замене позиций берутся текущие цены.
Деньги хранятся в `NUMERIC(12,2)`. Процентная скидка выше 70% не применяется,
фиксированная не превышает сумму заказа. У промокода `active` по умолчанию `true`.
При обновлении заказа промокод проверяется заново; если сумма стала ниже минимума,
скидка снимается и использование промокода возвращается.

Ошибки: `{"error_code":"…","message":"…","details":{…}}`.
`details.fields` содержит нарушения валидации, `details.products` — нехватку остатков.
Каждый запрос получает `X-Request-Id` и запись в JSON-логе. Пароли и токены маскируются.

## Проверки

| Команда | Что выполняет |
|---|---|
| `make generate` | Генерация ogen и protobuf/gRPC |
| `make build` | Генерация и сборка |
| `make check` | Генерация, Go race tests, vet и проверки CLI |
| `make test-scripts` | Проверки Python CLI без Docker и сети |
| `make e2e` | HTTP-сценарии и SELECT из баз Compose |

Для локальной генерации нужны Go и `protoc`. Сгенерированный код исключен из Git
и создается при `make build` и сборке Docker-образа. Python-скрипты используют
стандартную библиотеку.

Go-тестам БД нужны три тестовые базы с миграциями. Для Catalog и Orders требуется
`max_prepared_transactions=100`. Пример для локального Compose:

```sh
docker compose stop gateway orders catalog users
TEST_USERS_DATABASE_URL='postgres://users:users@localhost:5433/users?sslmode=disable' \
TEST_CATALOG_DATABASE_URL='postgres://catalog:catalog@localhost:5434/catalog?sslmode=disable' \
TEST_ORDERS_DATABASE_URL='postgres://orders:orders@localhost:5435/orders?sslmode=disable' \
make check
docker compose up -d --wait
make e2e
```

[E2E](scripts/e2e.py) проверяет права, валидацию, скидки, откаты, конкурентные запросы,
логи и состояние 2PC. Скрипт выводит SELECT из `products`, `orders`, `order_items`,
`promo_codes`, `user_operations` и оставляет данные для просмотра. Нужен
`ORDER_RATE_LIMIT_MINUTES=1`: время операций тестовых пользователей сдвигается через SQL.
Запускайте E2E без других тестов и клиентов в том же Compose-проекте.

[Тест восстановления](scripts/2pc-recovery.py) завершает Catalog и Orders в пяти
точках 2PC, перезапускает PostgreSQL и проверяет восстановление. Он допускает только
отдельный проект `marketplace-2pc-check`, в котором не должны работать другие клиенты:

```sh
MARKETPLACE_PORT=28080 USERS_POSTGRES_PORT=25431 \
CATALOG_POSTGRES_PORT=25432 ORDERS_POSTGRES_PORT=25433 \
docker compose -p marketplace-2pc-check up --build -d --wait --wait-timeout 180
python3 scripts/e2e.py --base-url http://localhost:28080 --project marketplace-2pc-check
python3 scripts/2pc-recovery.py --base-url http://localhost:28080 --project marketplace-2pc-check
```

## Архитектура

Общая модель маркетплейса включает Gateway, Users, Catalog, Orders, Payments,
Notifications и RabbitMQ. В ДЗ 2 реализованы Gateway, Users, Catalog и Orders.

![C4 Container — весь маркетплейс](docs/architecture/future/c4-container.svg)

![Легенда C4 Container](docs/architecture/future/c4-container-key.svg)

[Описание общей модели](docs/architecture/future/README.md) ·
[DSL](docs/architecture/future/workspace.dsl) · [PNG](docs/architecture/future/c4-container.png)

![C4 — сбор трасс маркетплейса](docs/architecture/future/c4-tracing.svg)

Реализация ДЗ 2:

![C4 Container — ДЗ 2](docs/architecture/c4-container.svg)

[Координатор и восстановление](docs/architecture/c4-components.svg) ·
[DSL](docs/architecture/workspace.dsl)

Users хранит пользователей и токены, Catalog — товары и остатки, Orders — заказы,
промокоды и журнал 2PC. Gateway проверяет OpenAPI и JWT, затем вызывает сервис по gRPC.
Внешние ключи действуют внутри каждой базы. Межсервисные ссылки проверяет приложение;
межбазовых FK нет. Это отличие от схемы задания с общей БД.

Catalog и Orders подготавливают свои транзакции. После подтверждения обоих участников
Orders сохраняет решение `COMMIT` и фиксирует изменения в обеих базах. Recovery
завершает сохраненные решения после сбоев. 2PC удерживает блокировки до завершения
и не дает общего снимка чтения между базами.

При неподтвержденном результате API возвращает `503 TRANSACTION_PENDING` с UUID
транзакции и заказа. Это не означает откат. Проверяйте заказ через `GET /orders/{id}`;
до завершения recovery возможен `404`. Повтор HTTP-запроса создает новую операцию.

Редактор C4: `make architecture-up`, затем <http://localhost:8081>.
Экспорт после правок DSL: `make architecture-export`. Остановка: `make architecture-down`.
