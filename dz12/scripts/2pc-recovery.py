#!/usr/bin/env python3

import argparse
import concurrent.futures
import datetime as dt
import json
import subprocess
import time
import urllib.error
import urllib.request
import uuid
from dataclasses import dataclass
from decimal import Decimal


@dataclass(frozen=True)
class CrashScenario:

    name: str
    commits: bool
    catalog_prepared: bool
    orders_prepared: bool
    catalog_receipt: bool = False
    orders_receipt: bool = False
    restart_databases: bool = False

    @property
    def boundary_decision(self):
        return "COMMIT" if self.commits else "PREPARING"

    @property
    def recovered_decision(self):
        return "COMMIT" if self.commits else "ABORT"


SCENARIOS = (
    CrashScenario(
        name="before_prepare",
        commits=False,
        catalog_prepared=False,
        orders_prepared=False,
    ),
    CrashScenario(
        name="both_prepared",
        commits=False,
        catalog_prepared=True,
        orders_prepared=True,
        restart_databases=True,
    ),
    CrashScenario(
        name="decision_saved",
        commits=True,
        catalog_prepared=True,
        orders_prepared=True,
        restart_databases=True,
    ),
    CrashScenario(
        name="catalog_committed",
        commits=True,
        catalog_prepared=False,
        orders_prepared=True,
        catalog_receipt=True,
        restart_databases=True,
    ),
    CrashScenario(
        name="both_committed",
        commits=True,
        catalog_prepared=False,
        orders_prepared=False,
        catalog_receipt=True,
        orders_receipt=True,
    ),
)


@dataclass(frozen=True)
class ScenarioFixtures:
    user_id: str
    user_token: str
    product_id: str
    promo_id: str
    order_id: str
    original_transaction_ids: set[str]


def wait_for(label, predicate, timeout=45):
    deadline = time.monotonic() + timeout
    last_error = None
    while time.monotonic() < deadline:
        try:
            result = predicate()
            if result:
                return result
        except (subprocess.SubprocessError, urllib.error.URLError) as error:
            last_error = error
        time.sleep(0.2)
    raise AssertionError(f"timed out: {label}; last error: {last_error}")


class SqlGate:
    def __init__(self, runner, domain, table, event, condition):
        self.runner = runner
        self.domain = domain
        self.table = table
        self.name = "e2e_2pc_" + uuid.uuid4().hex[:12]
        self.key = int(uuid.uuid4().hex[:7], 16)
        self.holder = None
        self.holder_name = self.name + "_holder"
        self.blocked_pid = None
        self.runner.sql(
            domain,
            f"""
            CREATE FUNCTION {self.name}() RETURNS trigger LANGUAGE plpgsql AS $$
            BEGIN
                IF {condition} THEN
                    PERFORM pg_advisory_xact_lock({self.key}::bigint);
                END IF;
                RETURN NEW;
            END; $$;
            CREATE TRIGGER {self.name} BEFORE {event} ON {table}
            FOR EACH ROW EXECUTE FUNCTION {self.name}();
            """,
        )

    def arm(self):
        self.holder = subprocess.Popen(
            self.runner.psql_command(self.domain)
            + [
                "-c",
                f"""
                SET application_name='{self.holder_name}';
                SELECT pg_advisory_lock({self.key}::bigint);
                SELECT pg_sleep(180);
                """,
            ],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        wait_for(
            "test lock acquired",
            lambda: self.runner.sql(
                self.domain,
                f"""
                SELECT count(*)
                FROM pg_locks locks
                JOIN pg_stat_activity activity USING(pid)
                WHERE activity.application_name='{self.holder_name}'
                  AND locks.locktype='advisory'
                  AND locks.granted
                """,
            )
            == "1",
        )

    def is_blocked(self):
        backend_pid = self.runner.sql(
            self.domain,
            f"""
            SELECT pid FROM pg_locks
            WHERE locktype='advisory'
              AND classid=0 AND objid={self.key} AND objsubid=1
              AND NOT granted
            """,
        )
        if backend_pid:
            assert len(backend_pid.splitlines()) == 1, backend_pid
            self.blocked_pid = int(backend_pid)
            return True
        return False

    def remove(self):
        if self.blocked_pid is not None:
            self.runner.sql(
                self.domain,
                f"""
                SELECT pg_terminate_backend(pid) FROM pg_locks
                WHERE pid={self.blocked_pid}
                  AND locktype='advisory'
                  AND classid=0 AND objid={self.key} AND objsubid=1
                  AND NOT granted
                """,
            )
        self.runner.sql(
            self.domain,
            f"""
            SELECT pg_terminate_backend(pid) FROM pg_stat_activity
            WHERE application_name='{self.holder_name}'
            """,
        )
        if self.holder:
            self.holder.wait(timeout=10)
            self.holder = None
        self.runner.sql(
            self.domain,
            f"""
            SET statement_timeout='5s';
            DROP TRIGGER IF EXISTS {self.name} ON {self.table};
            DROP FUNCTION IF EXISTS {self.name}();
            """,
        )


class RecoveryRunner:
    def __init__(self, project, base_url):
        self.compose = ["docker", "compose", "-p", project]
        self.base_url = base_url
        self.run_id = uuid.uuid4().hex[:10]
        self.password = "Recovery-test-" + self.run_id

    def run_compose(self, *parts, timeout=60):
        return subprocess.run(
            self.compose + list(parts),
            check=True,
            text=True,
            capture_output=True,
            timeout=timeout,
        ).stdout.strip()

    def psql_command(self, domain):
        return self.compose + [
            "exec",
            "-T",
            domain + "-db",
            "psql",
            "-X",
            "-q",
            "-At",
            "-v",
            "ON_ERROR_STOP=1",
            "-U",
            domain,
            "-d",
            domain,
        ]

    def sql(self, domain, query):
        assert domain in ("users", "catalog", "orders")
        return subprocess.run(
            self.psql_command(domain) + ["-c", query],
            check=True,
            text=True,
            capture_output=True,
            timeout=15,
        ).stdout.strip()

    def request(self, method, path, body=None, token=None, expected=200):
        headers = {"Content-Type": "application/json"}
        if token:
            headers["Authorization"] = "Bearer " + token
        request = urllib.request.Request(
            self.base_url + path,
            method=method,
            headers=headers,
            data=None if body is None else json.dumps(body).encode(),
        )
        try:
            response = urllib.request.urlopen(request, timeout=65)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            raw_body = response.read()
            payload = json.loads(raw_body) if raw_body else None
            status = response.status
        if expected is not None:
            assert status == expected, (method, path, status, payload)
            return payload
        return status, payload

    def create_account(self, role):
        email = f"2pc-{self.run_id}-{uuid.uuid4().hex[:8]}@example.com"
        user = self.request(
            "POST",
            "/auth/register",
            {"email": email, "password": self.password, "role": role},
            expected=201,
        )
        tokens = self.request(
            "POST", "/auth/login", {"email": email, "password": self.password}
        )
        return user["id"], tokens["access_token"]

    def is_prepared(self, domain, transaction_id):
        global_transaction_id = f"marketplace:{domain}:{transaction_id}"
        return (
            self.sql(
                domain,
                f"""
                SELECT count(*) FROM pg_prepared_xacts
                WHERE database=current_database() AND gid='{global_transaction_id}'
                """,
            )
            == "1"
        )

    def has_receipt(self, domain, transaction_id):
        table = (
            "reservation_receipts" if domain == "catalog" else "transaction_receipts"
        )
        return (
            self.sql(
                domain, f"SELECT count(*) FROM {table} WHERE id='{transaction_id}'"
            )
            == "1"
        )

    def services_healthy(self):
        raw_status = self.run_compose("ps", "--all", "--format", "json")
        rows = (
            json.loads(raw_status)
            if raw_status.startswith("[")
            else [json.loads(line) for line in raw_status.splitlines()]
        )
        services = {row["Service"]: row for row in rows}
        return all(
            services.get(service, {}).get("Health") == "healthy"
            for service in (
                "users",
                "catalog",
                "orders",
                "gateway",
                "users-db",
                "catalog-db",
                "orders-db",
            )
        )

    def assert_ready(self):
        self.request("GET", "/health")
        assert (
            self.services_healthy()
        ), "start all services in the dedicated project first"
        assert (
            self.sql(
                "orders", "SELECT count(*) FROM transaction_journal WHERE NOT completed"
            )
            == "0"
        ), "unfinished operations before test"
        for domain in ("catalog", "orders"):
            assert (
                self.sql(
                    domain,
                    """
                    SELECT count(*) FROM pg_prepared_xacts
                    WHERE database=current_database()
                    """,
                )
                == "0"
            ), "prepared operations before test"
            assert (
                self.sql(
                    domain,
                    "SELECT count(*) FROM pg_trigger WHERE tgname LIKE 'e2e_2pc_%'",
                )
                == "0"
            ), "old test gates exist; inspect before retrying"

    def create_fixtures(self, scenario, seller_token):
        user_id, user_token = self.create_account("USER")
        product = self.request(
            "POST",
            "/products",
            {
                "name": f"2PC {scenario.name} {self.run_id}",
                "description": None,
                "price": 10,
                "stock": 10,
                "category": "2pc_" + self.run_id,
                "status": "ACTIVE",
            },
            token=seller_token,
            expected=201,
        )
        now = dt.datetime.now(dt.timezone.utc)
        promo = self.request(
            "POST",
            "/promo-codes",
            {
                "code": "C_" + uuid.uuid4().hex[:10].upper(),
                "discount_type": "PERCENTAGE",
                "discount_value": 10,
                "min_order_amount": 0,
                "max_uses": 1,
                "valid_from": (now - dt.timedelta(days=1)).isoformat(),
                "valid_until": (now + dt.timedelta(days=1)).isoformat(),
                "active": True,
            },
            token=seller_token,
            expected=201,
        )
        order = self.request(
            "POST",
            "/orders",
            {
                "items": [{"product_id": product["id"], "quantity": 2}],
                "promo_code": promo["code"],
            },
            token=user_token,
            expected=201,
        )
        return ScenarioFixtures(
            user_id=user_id,
            user_token=user_token,
            product_id=product["id"],
            promo_id=promo["id"],
            order_id=order["id"],
            original_transaction_ids=self.transaction_ids(order["id"]),
        )

    def transaction_ids(self, order_id):
        return set(
            self.sql(
                "orders",
                f"SELECT id FROM transaction_journal WHERE order_id='{order_id}'",
            ).splitlines()
        )

    def create_gate(self, scenario, fixtures):
        product_matches = f"strpos(NEW.payload::text, '{fixtures.product_id}') > 0"
        order_matches = f"NEW.order_id='{fixtures.order_id}'::uuid"
        if scenario.name == "before_prepare":
            return SqlGate(
                self, "catalog", "reservation_journal", "INSERT", product_matches
            )
        if scenario.name == "both_prepared":
            return SqlGate(
                self,
                "orders",
                "transaction_journal",
                "UPDATE OF decision",
                f"{order_matches} AND OLD.decision='PREPARING' AND NEW.decision='COMMIT'",
            )
        if scenario.name == "decision_saved":
            return SqlGate(
                self,
                "catalog",
                "reservation_journal",
                "UPDATE OF state",
                f"{product_matches} AND OLD.state='PREPARED' AND NEW.state='COMMIT'",
            )
        if scenario.name == "catalog_committed":
            return SqlGate(
                self,
                "catalog",
                "reservation_journal",
                "UPDATE OF result",
                f"""
                {product_matches} AND NEW.state='COMMIT'
                AND EXISTS(SELECT 1 FROM reservation_receipts WHERE id=NEW.id)
                """,
            )
        return SqlGate(
            self,
            "orders",
            "transaction_journal",
            "UPDATE OF completed",
            f"{order_matches} AND NEW.decision='COMMIT' AND NEW.completed",
        )

    def assert_boundary(self, scenario, fixtures):
        new_transaction_ids = (
            self.transaction_ids(fixtures.order_id) - fixtures.original_transaction_ids
        )
        assert len(new_transaction_ids) == 1, new_transaction_ids
        transaction_id = new_transaction_ids.pop()
        assert (
            self.sql(
                "orders",
                f"SELECT decision FROM transaction_journal WHERE id='{transaction_id}'",
            )
            == scenario.boundary_decision
        )
        assert self.is_prepared("catalog", transaction_id) == scenario.catalog_prepared
        assert self.is_prepared("orders", transaction_id) == scenario.orders_prepared
        assert self.has_receipt("catalog", transaction_id) == scenario.catalog_receipt
        assert self.has_receipt("orders", transaction_id) == scenario.orders_receipt
        return transaction_id

    def restart_databases(self, scenario, transaction_id):
        if not scenario.restart_databases:
            return
        self.run_compose("restart", "catalog-db", "orders-db")
        wait_for(
            "PostgreSQL restart",
            lambda: self.sql("catalog", "SELECT 1") == "1"
            and self.sql("orders", "SELECT 1") == "1",
        )
        assert self.is_prepared("catalog", transaction_id) == scenario.catalog_prepared
        assert self.is_prepared("orders", transaction_id) == scenario.orders_prepared
        assert (
            self.sql(
                "orders",
                f"SELECT decision FROM transaction_journal WHERE id='{transaction_id}'",
            )
            == scenario.boundary_decision
        )

    def journal_outcome(self, transaction_id):
        return self.sql(
            "orders",
            f"""
            SELECT decision || ':' || completed::text
            FROM transaction_journal WHERE id='{transaction_id}'
            """,
        )

    def assert_recovered(self, scenario, fixtures, transaction_id):
        decision = scenario.recovered_decision
        wait_for(
            "durable decision completed",
            lambda: self.journal_outcome(transaction_id) == decision + ":true",
        )
        assert (
            self.sql(
                "catalog",
                f"SELECT state FROM reservation_journal WHERE id='{transaction_id}'",
            )
            == decision
        )
        assert not self.is_prepared("catalog", transaction_id) and not self.is_prepared(
            "orders", transaction_id
        )
        assert (
            self.has_receipt("catalog", transaction_id) == scenario.commits
            and self.has_receipt("orders", transaction_id) == scenario.commits
        )
        self.assert_business_state(fixtures, scenario.commits)

    def assert_business_state(self, fixtures, committed):
        quantity = 4 if committed else 2
        stock = self.sql(
            "catalog",
            f"SELECT stock FROM products WHERE id='{fixtures.product_id}'",
        )
        assert stock == str(10 - quantity)
        order = self.request(
            "GET", "/orders/" + fixtures.order_id, token=fixtures.user_token
        )
        assert order["status"] == "CREATED", order
        assert (
            len(order["items"]) == 1 and order["items"][0]["quantity"] == quantity
        ), order
        assert Decimal(str(order["total_amount"])) == Decimal(9 * quantity), order
        assert Decimal(str(order["discount_amount"])) == Decimal(quantity), order
        promo_uses = self.sql(
            "orders",
            f"SELECT current_uses FROM promo_codes WHERE id='{fixtures.promo_id}'",
        )
        assert promo_uses == "1"
        create_operations = self.sql(
            "orders",
            f"""
            SELECT count(*) FROM user_operations
            WHERE user_id='{fixtures.user_id}' AND operation_type='CREATE_ORDER'
            """,
        )
        assert create_operations == "1"
        update_operations = self.sql(
            "orders",
            f"""
            SELECT count(*) FROM user_operations
            WHERE user_id='{fixtures.user_id}' AND operation_type='UPDATE_ORDER'
            """,
        )
        assert update_operations == ("1" if committed else "0")

    def assert_pending_response(self, future, fixtures, transaction_id):
        status, payload = future.result(timeout=5)
        assert status == 503 and payload["error_code"] == "TRANSACTION_PENDING", (
            status,
            payload,
        )
        assert payload["details"]["transaction_id"] == transaction_id, payload
        assert payload["details"]["order_id"] == fixtures.order_id, payload

    def assert_repeated_recovery(self, scenario, fixtures, transaction_id):
        self.run_compose("restart", "orders")
        wait_for("second Orders start", self.services_healthy)
        self.assert_business_state(fixtures, scenario.commits)
        assert (
            self.journal_outcome(transaction_id)
            == scenario.recovered_decision + ":true"
        )

    def run_scenario(self, scenario, seller_token):
        fixtures = self.create_fixtures(scenario, seller_token)
        gate = self.create_gate(scenario, fixtures)
        services_stopped = False
        gate_removed = False
        executor = concurrent.futures.ThreadPoolExecutor(max_workers=1)
        try:
            gate.arm()
            update_future = executor.submit(
                self.request,
                "PUT",
                "/orders/" + fixtures.order_id,
                {"items": [{"product_id": fixtures.product_id, "quantity": 4}]},
                token=fixtures.user_token,
                expected=None,
            )
            wait_for(scenario.name + " boundary", gate.is_blocked, timeout=8)
            transaction_id = self.assert_boundary(scenario, fixtures)
            print(
                f"{scenario.name}: boundary reached, transaction={transaction_id}",
                flush=True,
            )

            services_stopped = True
            self.run_compose("kill", "-s", "SIGKILL", "orders", "catalog")
            gate.remove()
            gate_removed = True
            self.restart_databases(scenario, transaction_id)
            self.run_compose("start", "catalog", "orders")
            wait_for("services healthy", self.services_healthy)
            services_stopped = False

            self.assert_recovered(scenario, fixtures, transaction_id)
            self.assert_pending_response(update_future, fixtures, transaction_id)
            self.assert_repeated_recovery(scenario, fixtures, transaction_id)
            print(
                f"PASS {scenario.name}: {scenario.recovered_decision}, "
                "stock/order/promo/operation counts correct after repeated recovery",
                flush=True,
            )
        finally:
            if not gate_removed:
                gate.remove()
            if services_stopped:
                self.run_compose("start", "catalog", "orders")
            executor.shutdown(wait=True, cancel_futures=True)

    def assert_no_unfinished_work(self):
        for domain in ("catalog", "orders"):
            assert (
                self.sql(
                    domain,
                    """
                    SELECT count(*) FROM pg_prepared_xacts
                    WHERE database=current_database()
                    """,
                )
                == "0"
            )
            assert (
                self.sql(
                    domain,
                    "SELECT count(*) FROM pg_trigger WHERE tgname LIKE 'e2e_2pc_%'",
                )
                == "0"
            )
        assert (
            self.sql(
                "orders", "SELECT count(*) FROM transaction_journal WHERE NOT completed"
            )
            == "0"
        )

    def run(self):
        self.assert_ready()
        _, seller_token = self.create_account("SELLER")
        for scenario in SCENARIOS:
            self.run_scenario(scenario, seller_token)
        self.assert_no_unfinished_work()
        print(
            "PASS: 5 crash boundaries, PostgreSQL restarts, repeated recovery; "
            f"fixtures retained: category=2pc_{self.run_id}"
        )


def main(argv=None):
    parser = argparse.ArgumentParser(
        description="Check recovery at five 2PC crash boundaries in the dedicated test project."
    )
    parser.add_argument("--project", required=True)
    parser.add_argument("--base-url", default="http://localhost:28080")
    args = parser.parse_args(argv)
    if args.project != "marketplace-2pc-check":
        parser.error("only the dedicated marketplace-2pc-check project is allowed")
    RecoveryRunner(args.project, args.base_url).run()


if __name__ == "__main__":
    main()
