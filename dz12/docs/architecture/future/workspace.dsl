workspace "Маркетплейс" "Каталог, заказы, платежи и уведомления." {
    !impliedRelationships false

    model {
        buyer = person "Покупатель" "Выбирает товары, оформляет и оплачивает заказы."
        seller = person "Продавец" "Управляет своими товарами и исполнением заказов."

        email = softwareSystem "Email-провайдер" "Принимает запросы на доставку писем." {
            tags "External"
        }
        psp = softwareSystem "Платежный провайдер" "Страница оплаты, обработка платежей и отправка результата." {
            tags "External"
        }

        marketplace = softwareSystem "Маркетплейс" "Каталог, персональная лента, пользователи, заказы, платежи и уведомления." {
            properties {
                "Контракты бизнес-API" ".proto с пакетами v1; Go-клиенты, интерфейсы серверов и дескрипторы Envoy из одной ревизии."
                "Проверки контрактов в CI" "Buf lint и breaking (FILE) относительно поддерживаемых релизов; контрактные тесты gRPC и HTTP/JSON через Envoy."
                "Доступ к бизнес-API" "mTLS: идентификатор из SAN сертификата; сервер разрешает только перечисленные RPC для вызывающего сервиса."
                "Права пользователя" "Токен Users, роль и владелец ресурса проверяются отдельно от сертификата сервиса."
            }
            web = container "Web SPA" "Витрина, профиль, заказы и кабинет продавца." "React / TypeScript"
            gateway = container "API Gateway" "HTTP/JSON ↔ gRPC по дескрипторам Protobuf. Лимиты запросов и активных RPC." "Envoy"
            users = container "Users Service" "Аккаунты, роли, контакты, предпочтения и согласия." "Go / gRPC"
            catalog = container "Catalog Service" "Карточки, цены, доступность и персональная лента." "Go / gRPC"
            orders = container "Orders Service" "Снимок покупки, итог и жизненный цикл заказа." "Go / gRPC + worker"
            payments = container "Payments Service" "Расчет суммы платежа, попытки, учет и сверка." "Go / gRPC + HTTPS + worker"
            notifications = container "Notifications Service" "Письма о статусах заказа, повторы и учет доставки." "Go / worker"

            collector = container "OTel Collector" "Принимает spans, собирает пакеты и отправляет в Jaeger. Буфер ограничен." "OpenTelemetry Collector" {
                tags "Telemetry"
            }
            jaeger = container "Jaeger" "Хранит трассы на диске и показывает время и ошибки вызовов." "Jaeger / встроенный Badger" {
                tags "Telemetry"
            }

            users_db = container "Users DB" "Аккаунты, роли, email, интересы и согласия. Владелец: Users Service." "PostgreSQL" {
                tags "Database"
            }
            catalog_db = container "Catalog DB" "Товары, категории, продавцы, цены и доступность. Владелец: Catalog Service." "PostgreSQL" {
                tags "Database"
            }
            orders_db = container "Orders DB" "Заказы, снимки цен, статусы, ссылка оплаты, события, outbox/inbox. Владелец: Orders Service." "PostgreSQL" {
                tags "Database"
            }
            payments_db = container "Payments DB" "Попытки, суммы, результаты, журнал, outbox/inbox. Владелец: Payments Service." "PostgreSQL" {
                tags "Database"
            }
            notifications_db = container "Notifications DB" "Задания, шаблоны, попытки, доставка, дедупликация. Владелец: Notifications Service." "PostgreSQL" {
                tags "Database"
            }
        }

        buyer -> web "Покупает через браузер" "HTTPS"
        seller -> web "Работает в кабинете" "HTTPS"
        web -> gateway "Обращается к API маркетплейса" "HTTPS/JSON, синхронно"
        gateway -> users "Вход, свой профиль и настройки; без повторов" "gRPC/Protobuf, API v1, mTLS"
        gateway -> catalog "Лента и свои товары; без повторов" "gRPC/Protobuf, API v1, mTLS"
        gateway -> orders "Свои заказы и URL оплаты; без повторов" "gRPC/Protobuf, API v1, mTLS"
        catalog -> users "Категории текущего пользователя; при сбое — общая лента" "gRPC/Protobuf, API v1, mTLS"
        orders -> catalog "Чтение снимка товаров и цен; не более 1 повтора" "gRPC/Protobuf, API v1, mTLS"
        orders -> notifications "OrderStatusChanged; очередь notifications.order-status" "AMQP 0-9-1 через RabbitMQ" {
            tags "Async"
        }
        orders -> orders_db "Читает и пишет собственные данные" "PostgreSQL/TCP" {
            tags "DataAccess"
        }
        orders -> payments "Оплата и URL; до 1 повтора с тем же order_id" "gRPC/Protobuf, API v1, mTLS"
        notifications -> users "Чтение email и согласия; повторы в задании" "gRPC/Protobuf, API v1, mTLS"

        users -> users_db "Читает и пишет собственные данные" "PostgreSQL/TCP" {
            tags "DataAccess"
        }
        catalog -> catalog_db "Читает и пишет собственные данные" "PostgreSQL/TCP" {
            tags "DataAccess"
        }
        payments -> orders "PaymentSucceeded / PaymentFailed; очередь orders.payment-results" "AMQP 0-9-1 через RabbitMQ" {
            tags "Async"
        }
        payments -> payments_db "Читает и пишет собственные данные" "PostgreSQL/TCP" {
            tags "DataAccess"
        }
        notifications -> notifications_db "Читает и пишет собственные данные" "PostgreSQL/TCP" {
            tags "DataAccess"
        }

        web -> psp "Переходит на страницу оплаты" "HTTPS, браузер"
        payments -> psp "Создает платеж и сверяет его состояние" "HTTPS/JSON, синхронно"
        psp -> payments "Отправляет подписанный webhook результата; асинхронно к созданию оплаты" "HTTPS callback"
        notifications -> email "Отправляет письмо" "HTTPS API, синхронно"

        gateway -> collector "Отправляет spans в фоне" "OTLP/gRPC + TLS" {
            tags "TraceExport"
        }
        users -> collector "Отправляет spans в фоне" "OTLP/gRPC + TLS" {
            tags "TraceExport"
        }
        catalog -> collector "Отправляет spans в фоне" "OTLP/gRPC + TLS" {
            tags "TraceExport"
        }
        orders -> collector "Отправляет spans в фоне" "OTLP/gRPC + TLS" {
            tags "TraceExport"
        }
        payments -> collector "Отправляет spans в фоне" "OTLP/gRPC + TLS" {
            tags "TraceExport"
        }
        notifications -> collector "Отправляет spans в фоне" "OTLP/gRPC + TLS" {
            tags "TraceExport"
        }
        collector -> jaeger "Отправляет пакеты spans" "OTLP/gRPC + TLS" {
            tags "TraceExport"
        }
    }

    views {
        container marketplace "c4-container" "Сервисы, хранилища и взаимодействия маркетплейса." {
            title "Маркетплейс — C4 Container (уровень 2)"
            include *
            exclude collector jaeger
            !script layout.groovy
            properties {
                "structurizr.title" "false"
                "structurizr.description" "false"
                "structurizr.metadata" "false"
            }
        }

        container marketplace "c4-tracing" "Сбор и хранение трасс маркетплейса." {
            title "Маркетплейс — трассировка (C4 Container)"
            include gateway users catalog orders payments notifications collector jaeger
            exclude "relationship.tag!=TraceExport"
            !script tracing-layout.groovy
            properties {
                "structurizr.title" "false"
                "structurizr.description" "false"
                "structurizr.metadata" "false"
            }
        }

        styles {
            element "Element" {
                shape RoundedBox
                background #438DD5
                color #FFFFFF
                stroke #2465A1
                fontSize 24
                width 360
                height 240
            }
            element "Person" {
                shape Person
                background #17456B
            }
            element "External" {
                background #64748B
                stroke #475569
            }
            element "Database" {
                shape Cylinder
                background #DCEBFA
                color #15324F
                stroke #438DD5
                height 280
            }
            element "Telemetry" {
                background #D7F0E5
                color #153F36
                stroke #287A61
            }
            relationship "Relationship" {
                color #475569
                style solid
                thickness 2
                fontSize 22
                width 320
            }
            relationship "Async" {
                color #9333EA
                style dashed
            }
            relationship "DataAccess" {
                color #38708F
            }
            relationship "TraceExport" {
                color #287A61
                style dashed
            }
        }
    }
}
