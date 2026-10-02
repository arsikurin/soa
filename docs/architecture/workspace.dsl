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
            web = container "Web SPA" "Витрина, профиль, заказы и кабинет продавца." "React / TypeScript"
            users = container "Users Service" "Аккаунты, роли, контакты, предпочтения и согласия." "Go / HTTP API"
            catalog = container "Catalog Service" "Карточки, цены, доступность и персональная лента." "Go / HTTP API"
            orders = container "Orders Service" "Снимок покупки, итог и жизненный цикл заказа." "Go / HTTP API + worker"
            payments = container "Payments Service" "Расчет суммы платежа, попытки, учет и сверка." "Go / HTTP API + worker"
            notifications = container "Notifications Service" "Письма о статусах заказа, повторы и учет доставки." "Go / worker"

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
        web -> users "Вход, профиль и настройки" "HTTPS/JSON, синхронно"
        web -> catalog "Лента и управление товарами" "HTTPS/JSON, синхронно"
        web -> orders "Создание и статус заказа; получение ссылки оплаты" "HTTPS/JSON, синхронно"
        catalog -> users "Запрашивает любимые категории" "HTTPS/JSON, синхронно"
        orders -> catalog "Получает снимок товаров и цен" "HTTPS/JSON, синхронно"
        orders -> notifications "OrderStatusChanged; очередь notifications.order-status" "AMQP 0-9-1 через RabbitMQ" {
            tags "Async"
        }
        orders -> orders_db "Читает и пишет собственные данные" "PostgreSQL/TCP" {
            tags "DataAccess"
        }
        orders -> payments "Создает оплату, получает URL; Idempotency-Key: order_id" "HTTPS/JSON, синхронно"
        notifications -> users "Получает email и согласие" "HTTPS/JSON, синхронно"

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
    }

    views {
        container marketplace "c4-container" "Сервисы, хранилища и взаимодействия маркетплейса." {
            title "Маркетплейс — C4 Container (уровень 2)"
            include *
            !script layout.groovy
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
        }
    }
}
