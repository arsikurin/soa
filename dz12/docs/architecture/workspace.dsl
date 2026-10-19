workspace "Marketplace API — ДЗ 2" "Gateway, три доменных сервиса и отдельные базы; Orders и Catalog согласуют заказ через 2PC." {
    !impliedRelationships false
    model {
        buyer = person "Покупатель" "Читает каталог и управляет своими заказами."
        seller = person "Продавец" "Управляет своими товарами и создаёт промокоды."
        admin = person "Администратор" "Управляет товарами и заказами всех пользователей."
        marketplace = softwareSystem "Marketplace" "Контрактный API маркетплейса." {
            gateway = container "API Gateway" "Маршруты и валидация OpenAPI, JWT, ошибки и JSON-логи. Своей БД нет." "Go / ogen"
            users = container "Users" "Регистрация, bcrypt, access JWT и ротация refresh token." "Go / gRPC"
            catalog = container "Catalog" "Товары и остатки. Участник 2PC: резервирование, prepare и исполнение решения." "Go / gRPC / pgx"
            orders = container "Orders" "Заказы и промокоды. Координатор 2PC с долговечным решением и периодическим recovery." "Go / gRPC / pgx" {
                business = component "Заказы и промокоды" "Права, состояния, снимки цен, скидки и лимиты операций." "Go / decimal"
                coordinator = component "Координатор 2PC" "Журналирует операцию; готовит обоих участников; сохраняет COMMIT или ABORT и завершает их." "Go / pgx"
                recovery = component "Восстановление" "При запуске и периодически завершает решения; откатывает брошенные PREPARING под session lock." "Go"
            }
            usersDB = container "Users DB" "users, refresh_tokens." "PostgreSQL 18" {
                tags "Database"
            }
            catalogDB = container "Catalog DB" "products, reservation_journal, reservation_receipts; prepared transactions." "PostgreSQL 18" {
                tags "Database"
            }
            ordersDB = container "Orders DB" "orders, order_items, promo_codes, user_operations; журнал и квитанции 2PC." "PostgreSQL 18" {
                tags "Database"
            }
        }
        buyer -> gateway "Каталог и свои заказы" "HTTP/JSON + JWT"
        seller -> gateway "Свои товары и промокоды" "HTTP/JSON + JWT"
        admin -> gateway "Управление товарами и заказами" "HTTP/JSON + JWT"
        gateway -> users "Регистрация, вход, refresh" "gRPC / mTLS"
        gateway -> catalog "CRUD и чтение товаров" "gRPC / mTLS"
        gateway -> orders "Заказы и промокоды" "gRPC / mTLS"
        catalog -> users "Проверяет продавца при назначении" "gRPC / mTLS"
        orders -> catalog "Готовит резерв; исполняет решение" "gRPC / mTLS, 2PC"
        users -> usersDB "Только данные Users" "SQL / pgx"
        catalog -> catalogDB "Товары; PREPARE и COMMIT/ROLLBACK PREPARED" "SQL / pgx"
        orders -> ordersDB "Заказы, скидки, журнал решений и prepared transaction" "SQL / pgx"
        gateway -> business "Передаёт DTO и проверенную личность" "gRPC / mTLS"
        business -> coordinator "Создать, обновить или отменить заказ" "Вызов Go"
        business -> ordersDB "Читает заказы; меняет статус и промокоды" "SQL / pgx"
        coordinator -> ordersDB "Session lock; бизнес-транзакция, решение и квитанция" "SQL / pgx"
        coordinator -> catalog "Подготовка резерва и повторяемое завершение" "gRPC / mTLS"
        recovery -> ordersDB "Незавершённые решения; неблокирующий session lock" "SQL / pgx"
        recovery -> coordinator "Завершает COMMIT / ABORT" "Вызов Go"
    }
    views {
        container marketplace "c4-container" "Реализация ДЗ 2: отдельно запущенные сервисы и их базы." {
            include *
            !script layout.groovy
            properties {
                "structurizr.metadata" "false"
            }
        }
        component orders "c4-components" "Заказ и резерв в двух базах: координация и восстановление." {
            include gateway catalog business coordinator recovery ordersDB
            !script components-layout.groovy
            properties {
                "structurizr.metadata" "false"
            }
        }
        styles {
            element "Element" {
                shape RoundedBox
                background #438DD5
                color #FFFFFF
                fontSize 24
                width 440
                height 300
            }
            element "Person" {
                shape Person
                background #17456B
            }
            element "Database" {
                shape Cylinder
                background #DCEBFA
                color #15324F
            }
            relationship "Relationship" {
                color #475569
                fontSize 22
                style solid
            }
        }
    }
}
