#!/usr/bin/env python3

from __future__ import annotations

import argparse
import concurrent.futures
import datetime as dt
import json
import os
import subprocess
import threading
import urllib.error
import urllib.request
import uuid
from dataclasses import dataclass
from decimal import Decimal
from typing import Any


@dataclass(frozen=True)
class Account:
    id: str
    email: str
    access_token: str
    refresh_token: str


@dataclass(frozen=True)
class Actors:
    seller: Account
    other_seller: Account
    customer: Account
    other_customer: Account
    admin_token: str


@dataclass(frozen=True)
class Products:
    active: dict
    inactive: dict
    archived: dict


@dataclass(frozen=True)
class HTTPResponse:
    status: int
    payload: Any


def uuid_list(values) -> str:
    return ", ".join(f"'{uuid.UUID(value)}'" for value in values)


def assert_money(actual, expected) -> None:
    actual_amount = Decimal(str(actual))
    expected_amount = Decimal(str(expected))
    assert actual_amount == expected_amount, f"Expected amount {expected}, got {actual}"


class MarketplaceChecks:
    def __init__(self, base_url: str, project: str):
        self.base_url = base_url.rstrip("/")
        self.compose = ["docker", "compose", "-p", project]
        self.run_id = uuid.uuid4().hex[:10]
        self.category = "e2e_" + self.run_id
        self.password = "Test-pass-" + self.run_id
        self.user_ids: list[str] = []
        self.secrets = {self.password}
        self.request_ids: set[str] = set()
        self.request_lock = threading.Lock()

    def send(self, method, path, body=None, token=None) -> HTTPResponse:
        if body is None or isinstance(body, bytes):
            data = body
        else:
            data = json.dumps(body).encode()
        headers = {"Content-Type": "application/json"}
        if token:
            headers["Authorization"] = "Bearer " + token
        request = urllib.request.Request(
            self.base_url + path, data=data, method=method, headers=headers
        )
        try:
            response = urllib.request.urlopen(request, timeout=20)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            raw = response.read()
            payload = json.loads(raw) if raw else None
            status = response.status
            request_id = response.headers.get("X-Request-Id")
        uuid.UUID(request_id)
        with self.request_lock:
            self.request_ids.add(request_id)
        return HTTPResponse(status, payload)

    def request(
        self,
        method,
        path,
        body=None,
        token=None,
        *,
        expected_status=200,
        error_code=None,
    ):
        response = self.send(method, path, body, token)
        assert response.status == expected_status, (
            f"{method} {path}: expected {expected_status}, "
            f"got {response.status}: {response.payload}"
        )
        if error_code:
            assert response.payload["error_code"] == error_code, response.payload
        if error_code == "VALIDATION_ERROR":
            assert response.payload["details"]["fields"], response.payload
        label = f" {error_code}" if error_code else ""
        print(f"{method:6} {path:56} {response.status}{label}")
        return response.payload

    def sql(self, domain, query, *, display=False) -> str:
        assert domain in ("users", "catalog", "orders"), domain
        command = self.compose + [
            "exec",
            "-T",
            domain + "-db",
            "psql",
            "-X",
            "-v",
            "ON_ERROR_STOP=1",
            "-U",
            domain,
            "-d",
            domain,
        ]
        command += ["-c", query] if display else ["-At", "-c", query]
        result = subprocess.run(
            command, check=True, text=True, capture_output=True
        ).stdout.strip()
        if display:
            print(f"\nSQL [{domain}]> {query}\n{result}")
        return result

    def create_account(self, role="USER") -> Account:
        email = f"{self.run_id}-{uuid.uuid4().hex[:8]}@example.com"
        user = self.request(
            "POST",
            "/auth/register",
            {"email": email, "password": self.password, "role": role},
            expected_status=201,
        )
        tokens = self.request(
            "POST", "/auth/login", {"email": email, "password": self.password}
        )
        self.remember_tokens(tokens)
        self.user_ids.append(user["id"])
        return Account(
            user["id"], email, tokens["access_token"], tokens["refresh_token"]
        )

    def remember_tokens(self, tokens) -> None:
        self.secrets.update((tokens["access_token"], tokens["refresh_token"]))

    def create_product(self, token, *, price=100, stock=20, status="ACTIVE"):
        return self.request(
            "POST",
            "/products",
            {
                "name": "Товар " + self.run_id,
                "description": None,
                "price": price,
                "stock": stock,
                "category": self.category,
                "status": status,
            },
            token,
            expected_status=201,
        )

    def update_product(self, product, token, **changes):
        fields = ("name", "description", "price", "stock", "category", "status")
        body = {field: product[field] for field in fields}
        body.update(changes)
        return self.request("PUT", f"/products/{product['id']}", body, token)

    @staticmethod
    def order_body(product, quantity=1, *, promo_code=None):
        body = {"items": [{"product_id": product["id"], "quantity": quantity}]}
        if promo_code is not None:
            body["promo_code"] = promo_code
        return body

    def age_operations(self, account, operation):
        assert account.id in self.user_ids
        assert operation in ("CREATE_ORDER", "UPDATE_ORDER")
        self.sql(
            "orders",
            f"""
            UPDATE user_operations
            SET created_at = created_at - interval '2 days'
            WHERE user_id = '{account.id}' AND operation_type = '{operation}'
        """,
        )

    def create_promo(
        self,
        token,
        *,
        value=10,
        minimum=1000,
        maximum=20,
        kind="PERCENTAGE",
        **overrides,
    ):
        now = dt.datetime.now(dt.timezone.utc)
        body = {
            "code": "P_" + uuid.uuid4().hex[:10].upper(),
            "discount_type": kind,
            "discount_value": value,
            "min_order_amount": minimum,
            "max_uses": maximum,
            "valid_from": (now - dt.timedelta(days=1)).isoformat(),
            "valid_until": (now + dt.timedelta(days=1)).isoformat(),
        }
        body.update(overrides)
        promo = self.request("POST", "/promo-codes", body, token, expected_status=201)
        assert promo["active"] is overrides.get("active", True), promo
        return promo

    def assert_stock(self, product, expected):
        actual = self.sql(
            "catalog", f"SELECT stock FROM products WHERE id='{product['id']}'"
        )
        assert (
            int(actual) == expected
        ), f"Product {product['id']}: stock={actual}, want {expected}"

    def assert_promo_uses(self, promo, expected):
        actual = self.sql(
            "orders", f"SELECT current_uses FROM promo_codes WHERE id='{promo['id']}'"
        )
        assert (
            int(actual) == expected
        ), f"Promo {promo['code']}: uses={actual}, want {expected}"

    def check_authentication(self) -> Actors:
        self.request("GET", "/health")
        self.request(
            "GET", "/products", expected_status=401, error_code="TOKEN_INVALID"
        )
        self.request("GET", "/missing", expected_status=404, error_code="NOT_FOUND")
        self.request(
            "POST",
            "/auth/register",
            {
                "email": f"admin-{self.run_id}@example.com",
                "password": self.password,
                "role": "ADMIN",
            },
            expected_status=400,
            error_code="VALIDATION_ERROR",
        )
        seller = self.create_account("SELLER")
        other_seller = self.create_account("SELLER")
        customer = self.create_account()
        other_customer = self.create_account()
        admin_tokens = self.request(
            "POST",
            "/auth/login",
            {
                "email": os.getenv("BOOTSTRAP_ADMIN_EMAIL", "admin@example.com"),
                "password": os.getenv(
                    "BOOTSTRAP_ADMIN_PASSWORD", "Homework-Admin-Password-2026!"
                ),
            },
        )
        self.remember_tokens(admin_tokens)
        self.request(
            "POST",
            "/auth/login",
            {"email": customer.email, "password": "wrong-password"},
            expected_status=401,
            error_code="INVALID_CREDENTIALS",
        )
        self.request(
            "POST",
            "/auth/register",
            {"email": customer.email, "password": self.password},
            expected_status=409,
            error_code="EMAIL_ALREADY_EXISTS",
        )
        self.request(
            "GET",
            "/products",
            token=customer.refresh_token,
            expected_status=401,
            error_code="TOKEN_INVALID",
        )
        rotated = self.request(
            "POST", "/auth/refresh", {"refresh_token": customer.refresh_token}
        )
        self.remember_tokens(rotated)
        self.request(
            "POST",
            "/auth/refresh",
            {"refresh_token": customer.refresh_token},
            expected_status=401,
            error_code="REFRESH_TOKEN_INVALID",
        )
        self.request(
            "POST",
            "/auth/refresh",
            {"refresh_token": customer.access_token},
            expected_status=401,
            error_code="REFRESH_TOKEN_INVALID",
        )
        return Actors(
            seller, other_seller, customer, other_customer, admin_tokens["access_token"]
        )

    def check_products(self, actors: Actors) -> Products:
        seller_token = actors.seller.access_token
        customer_token = actors.customer.access_token
        active = self.create_product(seller_token)
        inactive = self.create_product(seller_token, status="INACTIVE")
        archived = self.create_product(seller_token)
        foreign = self.create_product(actors.other_seller.access_token)
        self.request(
            "DELETE",
            f"/products/{archived['id']}",
            token=seller_token,
            expected_status=204,
        )
        result = self.request(
            "GET", f"/products/{archived['id']}", token=customer_token
        )
        assert result["status"] == "ARCHIVED", result
        self.request(
            "DELETE",
            f"/products/{foreign['id']}",
            token=seller_token,
            expected_status=403,
            error_code="ACCESS_DENIED",
        )
        valid = {
            "name": "x",
            "price": 1,
            "stock": 0,
            "category": self.category,
            "status": "ACTIVE",
        }
        self.request(
            "POST",
            "/products",
            valid,
            customer_token,
            expected_status=403,
            error_code="ACCESS_DENIED",
        )
        self.request(
            "PUT",
            f"/products/{foreign['id']}",
            valid,
            seller_token,
            expected_status=403,
            error_code="ACCESS_DENIED",
        )
        self.request(
            "PUT",
            f"/products/{active['id']}",
            valid,
            customer_token,
            expected_status=403,
            error_code="ACCESS_DENIED",
        )
        self.request(
            "DELETE",
            f"/products/{active['id']}",
            token=customer_token,
            expected_status=403,
            error_code="ACCESS_DENIED",
        )

        invalid_fields = [
            {"name": "x\x00"},
            {"description": "\x00"},
            {"category": "\x00"},
            {"name": ""},
            {"name": "x" * 256},
            {"description": "x" * 4001},
            {"price": 0},
            {"price": 0.001},
            {"stock": -1},
            {"category": ""},
            {"category": "x" * 101},
            {"status": "INVALID"},
        ]
        for change in invalid_fields:
            self.request(
                "POST",
                "/products",
                {**valid, **change},
                seller_token,
                expected_status=400,
                error_code="VALIDATION_ERROR",
            )
        invalid_json = [
            b'{"price":1e100000}',
            b'{"price":1e-100000}',
            b'{"price":1e100000,"price":1}',
            b"[" * 200 + b"0" + b"]" * 200,
        ]
        for body in invalid_json:
            self.request(
                "POST",
                "/products",
                body,
                seller_token,
                expected_status=400,
                error_code="VALIDATION_ERROR",
            )
        for query in (
            "page=-1",
            "size=0",
            "size=101",
            "status=INVALID",
            "category=%00",
        ):
            self.request(
                "GET",
                "/products?" + query,
                token=customer_token,
                expected_status=400,
                error_code="VALIDATION_ERROR",
            )
        first_page = self.request(
            "GET",
            f"/products?category={self.category}&size=2&page=0",
            token=customer_token,
        )
        assert first_page["totalElements"] == 4, first_page
        assert first_page["page"] == 0 and first_page["size"] == 2, first_page
        assert len(first_page["items"]) == 2, first_page
        second_page = self.request(
            "GET",
            f"/products?category={self.category}&size=2&page=1",
            token=customer_token,
        )
        assert {item["id"] for item in first_page["items"]}.isdisjoint(
            item["id"] for item in second_page["items"]
        ), "Pages contain the same product"
        archived_page = self.request(
            "GET",
            f"/products?category={self.category}&status=ARCHIVED",
            token=customer_token,
        )
        assert archived_page["totalElements"] == 1, archived_page
        different_category = self.request(
            "GET", f"/products?category={self.category.upper()}", token=customer_token
        )
        assert different_category["totalElements"] == 0, different_category
        self.request(
            "GET",
            f"/products/{uuid.uuid4()}",
            token=customer_token,
            expected_status=404,
            error_code="PRODUCT_NOT_FOUND",
        )
        return Products(active, inactive, archived)

    def check_order_validation(self, actors: Actors, products: Products):
        customer_token = actors.customer.access_token
        self.request(
            "GET",
            f"/orders/{uuid.uuid4()}",
            token=customer_token,
            expected_status=404,
            error_code="ORDER_NOT_FOUND",
        )
        item = self.order_body(products.active)["items"]
        invalid_orders = [
            {"items": []},
            self.order_body(products.active, 0),
            self.order_body(products.active, 1000),
            {"items": item * 51},
            self.order_body(products.active, promo_code="bad"),
            {"items": item * 2},
        ]
        for body in invalid_orders:
            self.request(
                "POST",
                "/orders",
                body,
                customer_token,
                expected_status=400,
                error_code="VALIDATION_ERROR",
            )
        self.request(
            "POST",
            "/orders",
            self.order_body(products.active),
            actors.seller.access_token,
            expected_status=403,
            error_code="ACCESS_DENIED",
        )
        for product in (products.inactive, products.archived):
            self.request(
                "POST",
                "/orders",
                self.order_body(product),
                customer_token,
                expected_status=409,
                error_code="PRODUCT_INACTIVE",
            )
        self.request(
            "POST",
            "/orders",
            {"items": [{"product_id": str(uuid.uuid4()), "quantity": 1}]},
            customer_token,
            expected_status=404,
            error_code="PRODUCT_NOT_FOUND",
        )
        shortage = self.request(
            "POST",
            "/orders",
            self.order_body(products.active, 21),
            customer_token,
            expected_status=409,
            error_code="INSUFFICIENT_STOCK",
        )
        assert (
            shortage["details"]["products"][0]["product_id"] == products.active["id"]
        ), shortage
        self.request(
            "POST",
            "/orders",
            self.order_body(products.active, promo_code="MISSING"),
            customer_token,
            expected_status=422,
            error_code="PROMO_CODE_INVALID",
        )
        promo = self.create_promo(actors.seller.access_token)
        self.request(
            "POST",
            "/orders",
            self.order_body(products.active, promo_code=promo["code"]),
            customer_token,
            expected_status=422,
            error_code="PROMO_CODE_MIN_AMOUNT",
        )
        self.assert_stock(products.active, 20)
        operations = self.sql(
            "orders",
            f"SELECT count(*) FROM user_operations WHERE user_id='{actors.customer.id}'",
        )
        assert (
            operations == "0"
        ), "Rejected orders must not record successful operations"
        return promo

    def check_order_lifecycle(self, actors: Actors, products: Products, promo):
        product = products.active
        customer_token = actors.customer.access_token
        order = self.request(
            "POST",
            "/orders",
            self.order_body(product, 10, promo_code=promo["code"]),
            customer_token,
            expected_status=201,
        )
        order_path = f"/orders/{order['id']}"
        assert_money(order["total_amount"], 900)
        assert_money(order["discount_amount"], 100)
        self.assert_stock(product, 10)
        self.assert_promo_uses(promo, 1)
        self.request(
            "POST",
            "/orders",
            self.order_body(product),
            customer_token,
            expected_status=429,
            error_code="ORDER_LIMIT_EXCEEDED",
        )
        self.age_operations(actors.customer, "CREATE_ORDER")
        self.request(
            "POST",
            "/orders",
            self.order_body(product),
            customer_token,
            expected_status=409,
            error_code="ORDER_HAS_ACTIVE",
        )
        for method, path, body in [
            ("GET", order_path, None),
            ("PUT", order_path, self.order_body(product)),
            ("POST", order_path + "/cancel", None),
        ]:
            self.request(
                method,
                path,
                body,
                actors.other_customer.access_token,
                expected_status=403,
                error_code="ORDER_OWNERSHIP_VIOLATION",
            )
            self.request(
                method,
                path,
                body,
                actors.seller.access_token,
                expected_status=403,
                error_code="ACCESS_DENIED",
            )

        product = self.update_product(
            product, actors.seller.access_token, price=150, stock=10
        )
        snapshot = self.request("GET", order_path, token=customer_token)
        assert_money(snapshot["items"][0]["price_at_order"], 100)
        assert_money(snapshot["total_amount"], 900)
        self.request(
            "PUT",
            order_path,
            self.order_body(product, 30),
            customer_token,
            expected_status=409,
            error_code="INSUFFICIENT_STOCK",
        )
        self.assert_stock(product, 10)
        self.assert_promo_uses(promo, 1)
        updated = self.request(
            "PUT", order_path, self.order_body(product, 5), customer_token
        )
        assert_money(updated["total_amount"], 750)
        assert_money(updated["discount_amount"], 0)
        assert updated["promo_code_id"] is None, updated
        self.assert_stock(product, 15)
        self.assert_promo_uses(promo, 0)
        self.request(
            "PUT",
            order_path,
            self.order_body(product, 6),
            customer_token,
            expected_status=429,
            error_code="ORDER_LIMIT_EXCEEDED",
        )
        self.age_operations(actors.customer, "UPDATE_ORDER")
        self.request(
            "PUT",
            order_path,
            self.order_body(products.inactive),
            customer_token,
            expected_status=409,
            error_code="PRODUCT_INACTIVE",
        )
        self.assert_stock(product, 15)
        canceled = self.request("POST", order_path + "/cancel", token=customer_token)
        assert canceled["status"] == "CANCELED", canceled
        self.request(
            "POST",
            order_path + "/cancel",
            token=customer_token,
            expected_status=409,
            error_code="INVALID_STATE_TRANSITION",
        )
        self.assert_stock(product, 20)
        return product

    def check_discounts(self, actors: Actors, product):
        cases = [
            (70, "PERCENTAGE", 45, 105),
            (71, "PERCENTAGE", 150, 0),
            (500, "FIXED_AMOUNT", 0, 150),
        ]
        for value, kind, total, discount in cases:
            promo = self.create_promo(
                actors.seller.access_token, value=value, minimum=0, maximum=1, kind=kind
            )
            customer = self.create_account()
            order = self.request(
                "POST",
                "/orders",
                self.order_body(product, promo_code=promo["code"]),
                customer.access_token,
                expected_status=201,
            )
            assert_money(order["total_amount"], total)
            assert_money(order["discount_amount"], discount)
            order_path = f"/orders/{order['id']}"
            updated = self.request(
                "PUT", order_path, self.order_body(product), customer.access_token
            )
            assert updated["id"] == order["id"], updated
            self.assert_promo_uses(promo, 1)
            self.request("POST", order_path + "/cancel", token=customer.access_token)
            self.assert_promo_uses(promo, 0)

        expired = self.create_promo(
            actors.seller.access_token,
            minimum=0,
            valid_from="2020-01-01T00:00:00Z",
            valid_until="2020-01-02T00:00:00Z",
        )
        self.request(
            "POST",
            "/orders",
            self.order_body(product, promo_code=expired["code"]),
            actors.other_customer.access_token,
            expected_status=422,
            error_code="PROMO_CODE_INVALID",
        )
        disabled = self.create_promo(
            actors.seller.access_token, minimum=0, active=False
        )
        self.request(
            "POST",
            "/orders",
            self.order_body(product, promo_code=disabled["code"]),
            actors.other_customer.access_token,
            expected_status=422,
            error_code="PROMO_CODE_INVALID",
        )

    def check_order_states(self, actors: Actors, product):
        customer_token = actors.other_customer.access_token
        order = self.request(
            "POST",
            "/orders",
            self.order_body(product),
            customer_token,
            expected_status=201,
        )
        order_path = f"/orders/{order['id']}"
        self.request(
            "POST",
            order_path + "/status",
            {"status": "PAID"},
            actors.admin_token,
            expected_status=409,
            error_code="INVALID_STATE_TRANSITION",
        )
        self.request(
            "POST",
            order_path + "/status",
            {"status": "PAYMENT_PENDING"},
            customer_token,
            expected_status=403,
            error_code="ACCESS_DENIED",
        )
        for state in ("PAYMENT_PENDING", "PAID", "SHIPPED", "COMPLETED"):
            updated = self.request(
                "POST", order_path + "/status", {"status": state}, actors.admin_token
            )
            assert updated["status"] == state, updated
            self.request(
                "PUT",
                order_path,
                self.order_body(product),
                customer_token,
                expected_status=409,
                error_code="INVALID_STATE_TRANSITION",
            )
            if state != "PAYMENT_PENDING":
                self.request(
                    "POST",
                    order_path + "/cancel",
                    token=actors.admin_token,
                    expected_status=409,
                    error_code="INVALID_STATE_TRANSITION",
                )

    def race_orders(self, requests) -> list[HTTPResponse]:
        with concurrent.futures.ThreadPoolExecutor(max_workers=len(requests)) as pool:
            jobs = [
                pool.submit(self.send, "POST", "/orders", body, token)
                for token, body in requests
            ]
            return [job.result() for job in jobs]

    def check_concurrent_orders(self, actors: Actors, product):
        last_product = self.create_product(actors.seller.access_token, stock=1)
        customers = [self.create_account() for _ in range(4)]
        results = self.race_orders(
            [
                (customer.access_token, self.order_body(last_product))
                for customer in customers
            ]
        )
        assert sorted(result.status for result in results) == [
            201,
            409,
            409,
            409,
        ], results
        assert all(
            result.payload["error_code"] == "INSUFFICIENT_STOCK"
            for result in results
            if result.status == 409
        ), results
        self.assert_stock(last_product, 0)
        print("CONCURRENT last stock: 1 created, 3 rejected, stock=0")

        limited_promo = self.create_promo(
            actors.seller.access_token, minimum=0, maximum=1
        )
        products = [
            self.create_product(actors.seller.access_token, stock=1) for _ in range(2)
        ]
        customers = [self.create_account() for _ in range(2)]
        results = self.race_orders(
            [
                (
                    customer.access_token,
                    self.order_body(candidate, promo_code=limited_promo["code"]),
                )
                for customer, candidate in zip(customers, products)
            ]
        )
        assert sorted(result.status for result in results) == [201, 422], results
        rejected = next(result.payload for result in results if result.status == 422)
        assert rejected["error_code"] == "PROMO_CODE_INVALID", rejected
        self.assert_promo_uses(limited_promo, 1)
        product_ids = uuid_list(product["id"] for product in products)
        remaining = self.sql(
            "catalog", f"SELECT sum(stock) FROM products WHERE id IN ({product_ids})"
        )
        assert remaining == "1", "Failed promo reservation must return its stock"
        print(
            "CONCURRENT last promo: 1 created, 1 rejected; failed reservation rolled back"
        )

        customer = self.create_account()
        results = self.race_orders(
            [(customer.access_token, self.order_body(product)) for _ in range(2)]
        )
        assert sorted(result.status for result in results) == [201, 429], results
        rejected = next(result.payload for result in results if result.status == 429)
        assert rejected["error_code"] == "ORDER_LIMIT_EXCEEDED", rejected
        print("CONCURRENT same user: 1 created, 1 rate-limited")

    def inspect_business_tables(self):
        self.sql(
            "catalog",
            f"""
            SELECT id,name,price,stock,status,seller_id,created_at,updated_at
            FROM products WHERE category='{self.category}' ORDER BY created_at
        """,
            display=True,
        )
        user_ids = uuid_list(self.user_ids)
        queries = [
            f"""
            SELECT id, user_id, status, total_amount, discount_amount, promo_code_id
            FROM orders
            WHERE user_id IN ({user_ids})
            ORDER BY created_at
            """,
            f"""
            SELECT items.* FROM order_items items
            JOIN orders ON orders.id = items.order_id
            WHERE orders.user_id IN ({user_ids})
            ORDER BY items.order_id
            """,
            f"""
            SELECT code, discount_type, discount_value, current_uses, max_uses
            FROM promo_codes
            WHERE id IN (
                SELECT promo_code_id FROM orders WHERE user_id IN ({user_ids})
            )
            """,
            f"""
            SELECT user_id, operation_type, created_at FROM user_operations
            WHERE user_id IN ({user_ids})
            ORDER BY created_at
            """,
        ]
        for query in queries:
            self.sql("orders", query, display=True)

    def transaction_ids(self):
        return set(
            self.sql("orders", "SELECT id FROM transaction_journal").splitlines()
        )

    def check_two_phase_commit(self, transactions_before):
        transaction_ids = sorted(self.transaction_ids() - transactions_before)
        assert transaction_ids, "No 2PC transactions recorded"
        ids = uuid_list(transaction_ids)
        checks = [
            (
                "orders",
                f"""
                SELECT count(*) FROM transaction_journal WHERE id IN ({ids})
                AND (NOT completed OR decision='PREPARING')
            """,
                0,
                "All decisions must be completed",
            ),
            (
                "orders",
                f"""
                SELECT count(*) FROM transaction_journal j
                LEFT JOIN transaction_receipts r ON r.id=j.id
                WHERE j.id IN ({ids}) AND ((j.decision='COMMIT') <> (r.id IS NOT NULL))
            """,
                0,
                "Orders receipts must match COMMIT decisions",
            ),
            (
                "catalog",
                f"SELECT count(*) FROM reservation_journal WHERE id IN ({ids})",
                len(transaction_ids),
                "Every coordinator operation must have a Catalog decision",
            ),
            (
                "catalog",
                f"""
                SELECT count(*) FROM reservation_journal j
                LEFT JOIN reservation_receipts r ON r.id=j.id WHERE j.id IN ({ids})
                AND (j.state NOT IN ('COMMIT','ABORT') OR ((j.state='COMMIT') <> (r.id IS NOT NULL)))
            """,
                0,
                "Catalog decisions must be final and match receipts",
            ),
        ]
        for domain, query, expected, message in checks:
            assert int(self.sql(domain, query)) == expected, message
        self.sql(
            "orders",
            f"""
            SELECT j.id,j.order_id,j.decision,j.completed,r.id IS NOT NULL AS commit_receipt
            FROM transaction_journal j LEFT JOIN transaction_receipts r ON r.id=j.id
            WHERE j.id IN ({ids}) ORDER BY j.created_at
        """,
            display=True,
        )
        self.sql(
            "catalog",
            f"""
            SELECT j.id,j.state,r.id IS NOT NULL AS commit_receipt
            FROM reservation_journal j LEFT JOIN reservation_receipts r ON r.id=j.id
            WHERE j.id IN ({ids}) ORDER BY j.created_at
        """,
            display=True,
        )
        for domain in ("catalog", "orders"):
            gids = ", ".join(
                f"'marketplace:{domain}:{transaction_id}'"
                for transaction_id in transaction_ids
            )
            remaining = self.sql(
                domain,
                f"""
                SELECT count(*) FROM pg_prepared_xacts
                WHERE database=current_database() AND gid IN ({gids})
            """,
            )
            assert remaining == "0", f"Prepared transactions remain in {domain}"
            self.sql(
                domain,
                """
                SELECT gid, prepared, owner, database FROM pg_prepared_xacts
                WHERE database = current_database()
                """,
                display=True,
            )
        print(
            f"2PC: {len(transaction_ids)} completed decisions, matching commit receipts, no remaining prepared transactions for this run"
        )

    def check_request_logs(self):
        logs = subprocess.run(
            self.compose + ["logs", "--no-log-prefix", "gateway"],
            check=True,
            text=True,
            capture_output=True,
        ).stdout
        assert all(
            secret not in logs for secret in self.secrets
        ), "Credentials leaked into logs"
        events = {}
        for line in logs.splitlines():
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                continue
            if "request_id" in event:
                events[event["request_id"]] = event
        missing = self.request_ids - events.keys()
        assert not missing, f"Missing request logs: {missing}"
        fields = (
            "request_id",
            "method",
            "endpoint",
            "status_code",
            "duration_ms",
            "user_id",
            "timestamp",
        )
        for request_id in self.request_ids:
            event = events[request_id]
            assert all(field in event for field in fields), event

    def run(self):
        transactions_before = self.transaction_ids()
        actors = self.check_authentication()
        products = self.check_products(actors)
        promo = self.check_order_validation(actors, products)
        product = self.check_order_lifecycle(actors, products, promo)
        self.check_discounts(actors, product)
        self.check_order_states(actors, product)
        self.check_concurrent_orders(actors, product)
        self.inspect_business_tables()
        self.check_two_phase_commit(transactions_before)
        self.check_request_logs()
        print(
            f"\nPASS: {len(self.request_ids)} HTTP requests, concurrent operations, "
            "SQL checks, JSON logs and secret masking. "
            f"Fixtures retained: category={self.category}"
        )


def main():
    parser = argparse.ArgumentParser(
        description="Run marketplace API scenarios and inspect their database records."
    )
    parser.add_argument(
        "--base-url", default=os.getenv("BASE_URL", "http://localhost:8080")
    )
    parser.add_argument(
        "--project", default=os.getenv("COMPOSE_PROJECT_NAME", "marketplace-homework")
    )
    args = parser.parse_args()
    MarketplaceChecks(args.base_url, args.project).run()


if __name__ == "__main__":
    main()
