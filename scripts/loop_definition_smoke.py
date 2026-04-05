#!/usr/bin/env python3

from __future__ import annotations

import argparse
import json
import subprocess
import sys
import time
import urllib.error
import urllib.request
from dataclasses import dataclass
from datetime import datetime, timedelta
from typing import Any
from zoneinfo import ZoneInfo


DAY_NAMES = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"]


@dataclass
class APIResponse:
    status: int
    body: Any


class SmokeFailure(RuntimeError):
    pass


class SmokeClient:
    def __init__(self, base_url: str, timeout_seconds: float) -> None:
        self.base_url = base_url.rstrip("/")
        self.timeout_seconds = timeout_seconds

    def request(self, method: str, path: str, payload: Any | None = None) -> APIResponse:
        data = None
        headers: dict[str, str] = {}
        if payload is not None:
            data = json.dumps(payload).encode("utf-8")
            headers["Content-Type"] = "application/json"
        req = urllib.request.Request(
            self.base_url + path,
            data=data,
            headers=headers,
            method=method,
        )
        try:
            with urllib.request.urlopen(req, timeout=self.timeout_seconds) as resp:
                return APIResponse(resp.status, json.load(resp))
        except urllib.error.HTTPError as err:
            raw = err.read().decode("utf-8", errors="replace")
            try:
                body = json.loads(raw)
            except json.JSONDecodeError:
                body = {"raw": raw}
            return APIResponse(err.code, body)

    def get(self, path: str) -> APIResponse:
        return self.request("GET", path)

    def post(self, path: str, payload: Any) -> APIResponse:
        return self.request("POST", path, payload)

    def delete(self, path: str) -> APIResponse:
        return self.request("DELETE", path)


def require(condition: bool, message: str) -> None:
    if not condition:
        raise SmokeFailure(message)


def chicago_days() -> tuple[str, str]:
    now = datetime.now(ZoneInfo("America/Chicago"))
    today = DAY_NAMES[now.weekday()]
    tomorrow = DAY_NAMES[(now + timedelta(days=1)).weekday()]
    return today, tomorrow


def definition_names() -> tuple[str, str]:
    suffix = datetime.now().strftime("%Y%m%d%H%M%S")
    return (f"loop_def_smoke_active_{suffix}", f"loop_def_smoke_ineligible_{suffix}")


def build_service_spec(name: str, day: str, task: str) -> dict[str, Any]:
    return {
        "name": name,
        "enabled": True,
        "task": task,
        "profile": {
            "mission": "background",
            "delegation_gating": "disabled",
        },
        "operation": "service",
        "completion": "none",
        "conditions": {
            "schedule": {
                "timezone": "America/Chicago",
                "windows": [
                    {
                        "days": [day],
                        "start": "00:00",
                        "end": "23:59",
                    }
                ],
            }
        },
        "sleep_min": "10m",
        "sleep_max": "10m",
        "sleep_default": "10m",
    }


def print_step(message: str) -> None:
    print(f"[loop-def-smoke] {message}", flush=True)


def wait_for(
    timeout_seconds: float,
    interval_seconds: float,
    description: str,
    predicate,
) -> Any:
    deadline = time.time() + timeout_seconds
    last_error: Exception | None = None
    while time.time() < deadline:
        try:
            value = predicate()
        except Exception as err:  # noqa: BLE001 - surface the final predicate error
            last_error = err
            time.sleep(interval_seconds)
            continue
        if value:
            return value
        time.sleep(interval_seconds)
    if last_error is not None:
        raise SmokeFailure(f"timed out waiting for {description}: {last_error}")
    raise SmokeFailure(f"timed out waiting for {description}")


def fetch_definition(client: SmokeClient, name: str) -> dict[str, Any] | None:
    resp = client.get(f"/v1/loop-definitions/{name}")
    if resp.status == 404:
        return None
    require(resp.status == 200, f"GET /v1/loop-definitions/{name} returned {resp.status}: {resp.body}")
    return resp.body


def wait_for_definition_state(
    client: SmokeClient,
    name: str,
    *,
    policy_state: str | None = None,
    eligible: bool | None = None,
    running: bool | None = None,
    timeout_seconds: float,
) -> dict[str, Any]:
    def predicate() -> dict[str, Any] | None:
        definition = fetch_definition(client, name)
        if definition is None:
            return None
        if policy_state is not None and definition.get("policy_state") != policy_state:
            return None
        eligibility = definition.get("eligibility", {})
        runtime = definition.get("runtime", {})
        if eligible is not None and eligibility.get("eligible") is not eligible:
            return None
        if running is not None and runtime.get("running") is not running:
            return None
        return definition

    return wait_for(timeout_seconds, 0.5, f"{name} state", predicate)


def wait_for_server(client: SmokeClient, timeout_seconds: float) -> dict[str, Any]:
    def predicate() -> dict[str, Any] | None:
        resp = client.get("/v1/version")
        if resp.status != 200:
            return None
        return resp.body

    return wait_for(timeout_seconds, 1.0, "API server readiness", predicate)


def cleanup_definitions(client: SmokeClient, names: list[str]) -> None:
    for name in names:
        definition = fetch_definition(client, name)
        if definition is None:
            continue
        resp = client.delete(f"/v1/loop-definitions/{name}")
        require(resp.status == 200, f"DELETE /v1/loop-definitions/{name} returned {resp.status}: {resp.body}")
        wait_for(
            20,
            0.5,
            f"{name} deletion",
            lambda: fetch_definition(client, name) is None,
        )


def run_restart(restart_cmd: str) -> None:
    print_step(f"running restart command: {restart_cmd}")
    completed = subprocess.run(restart_cmd, shell=True, check=False)  # noqa: S602 - intentional local operator command
    require(completed.returncode == 0, f"restart command failed with exit code {completed.returncode}")


def main() -> int:
    parser = argparse.ArgumentParser(description="Live smoke test for loops-ng loop definition registry behavior.")
    parser.add_argument("--base-url", default="http://127.0.0.1:8080", help="Base URL for the live Thane API.")
    parser.add_argument(
        "--restart-cmd",
        default="",
        help="Optional shell command that restarts the live dev instance for persistence testing.",
    )
    parser.add_argument(
        "--timeout-seconds",
        type=float,
        default=30.0,
        help="Timeout for API polling and live state transitions.",
    )
    args = parser.parse_args()

    client = SmokeClient(args.base_url, timeout_seconds=10.0)
    eligible_name, ineligible_name = definition_names()
    names = [eligible_name, ineligible_name]
    today, tomorrow = chicago_days()

    print_step(f"base_url={args.base_url}")
    print_step(f"eligible day={today}, ineligible day={tomorrow}")

    try:
        version = wait_for_server(client, args.timeout_seconds)
        print_step(f"connected to version {version['version']} ({version['git_commit']})")

        baseline = client.get("/v1/loop-definitions")
        require(baseline.status == 200, f"GET /v1/loop-definitions returned {baseline.status}: {baseline.body}")
        print_step(
            f"baseline definitions={baseline.body.get('config_definitions', 0) + baseline.body.get('overlay_definitions', 0)}"
        )

        print_step("creating eligible service definition")
        resp = client.post(
            "/v1/loop-definitions",
            {"spec": build_service_spec(eligible_name, today, "Disposable eligible live smoke definition.")},
        )
        require(resp.status == 200, f"eligible create failed: {resp.status} {resp.body}")
        wait_for_definition_state(
            client,
            eligible_name,
            policy_state="active",
            eligible=True,
            running=True,
            timeout_seconds=args.timeout_seconds,
        )
        print_step("eligible definition is active and running")

        print_step("creating out-of-window service definition")
        resp = client.post(
            "/v1/loop-definitions",
            {"spec": build_service_spec(ineligible_name, tomorrow, "Disposable ineligible live smoke definition.")},
        )
        require(resp.status == 200, f"ineligible create failed: {resp.status} {resp.body}")
        wait_for_definition_state(
            client,
            ineligible_name,
            policy_state="active",
            eligible=False,
            running=False,
            timeout_seconds=args.timeout_seconds,
        )
        print_step("ineligible definition is retained and not running")

        print_step("pausing eligible definition")
        resp = client.post(
            "/v1/loop-definitions/policy",
            {"name": eligible_name, "state": "paused", "reason": "loop definition smoke pause"},
        )
        require(resp.status == 200, f"pause failed: {resp.status} {resp.body}")
        wait_for_definition_state(
            client,
            eligible_name,
            policy_state="paused",
            eligible=True,
            running=False,
            timeout_seconds=args.timeout_seconds,
        )
        print_step("paused definition stopped cleanly")

        print_step("resuming eligible definition")
        resp = client.post(
            "/v1/loop-definitions/policy",
            {"name": eligible_name, "state": "active", "reason": "loop definition smoke resume"},
        )
        require(resp.status == 200, f"resume failed: {resp.status} {resp.body}")
        wait_for_definition_state(
            client,
            eligible_name,
            policy_state="active",
            eligible=True,
            running=True,
            timeout_seconds=args.timeout_seconds,
        )
        print_step("resumed definition is running again")

        print_step("verifying ineligible launch fails honestly")
        resp = client.post(f"/v1/loop-definitions/{ineligible_name}/launch", {"launch": {}})
        require(resp.status == 409, f"ineligible launch should return 409, got {resp.status}: {resp.body}")
        error_message = json.dumps(resp.body)
        require(
            "not currently eligible" in error_message and "outside scheduled windows" in error_message,
            f"ineligible launch response missing expected detail: {resp.body}",
        )
        print_step("ineligible launch returned the expected 409 error")

        if args.restart_cmd:
            run_restart(args.restart_cmd)
            version = wait_for_server(client, args.timeout_seconds)
            print_step(f"server came back after restart on {version['git_commit']}")
            wait_for_definition_state(
                client,
                eligible_name,
                policy_state="active",
                eligible=True,
                running=True,
                timeout_seconds=args.timeout_seconds,
            )
            wait_for_definition_state(
                client,
                ineligible_name,
                policy_state="active",
                eligible=False,
                running=False,
                timeout_seconds=args.timeout_seconds,
            )
            print_step("definitions persisted and reconciled correctly after restart")

    finally:
        try:
            cleanup_definitions(client, names)
        except Exception as err:  # noqa: BLE001 - cleanup should still surface
            print_step(f"cleanup warning: {err}")

    final_view = client.get("/v1/loop-definitions")
    require(final_view.status == 200, f"final registry read failed: {final_view.status} {final_view.body}")
    remaining = {d["name"] for d in final_view.body.get("definitions", [])}
    require(not (remaining & set(names)), f"test definitions still present after cleanup: {sorted(remaining & set(names))}")
    print_step("smoke test passed")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except SmokeFailure as err:
        print(f"[loop-def-smoke] FAIL: {err}", file=sys.stderr, flush=True)
        raise SystemExit(1)
