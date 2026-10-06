"""Exercise both service metric initializers against an official OTLP collector."""

import argparse
import json
import os
from pathlib import Path
import socket
import shutil
import subprocess
import tempfile
import time
import urllib.error
import urllib.request


def address():
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return f"127.0.0.1:{listener.getsockname()[1]}"


def wait_ready(process, endpoint):
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError("collector exited before readiness")
        try:
            with urllib.request.urlopen(f"http://{endpoint}", timeout=0.2) as response:
                if response.status == 200:
                    return
        except (urllib.error.URLError, TimeoutError):
            pass
        time.sleep(0.05)
    raise RuntimeError("collector health extension did not become ready")


def verify(path):
    expected = {"accounts", "auth-gateway"}
    counts = {service: [] for service in expected}
    runtime = set()
    for line in path.read_text().splitlines():
        for resource in json.loads(line).get("resourceMetrics", []):
            attrs = {a["key"]: a["value"].get("stringValue") for a in resource["resource"]["attributes"]}
            service = attrs.get("service.name")
            if service not in expected:
                raise AssertionError(f"unexpected service identity: {service!r}")
            assert attrs.get("deployment.environment.name") == "qualification", attrs
            for scope in resource.get("scopeMetrics", []):
                for metric in scope.get("metrics", []):
                    if metric["name"] == "qualification.request.count":
                        counts[service].extend(int(point["asInt"]) for point in metric["sum"]["dataPoints"])
                    if metric["name"] == "go.goroutine.count":
                        runtime.add(service)
    assert all(values and all(value == 1 for value in values) for values in counts.values()), counts
    assert runtime == expected, runtime
    return {"services": sorted(expected), "counter_values": counts, "runtime_metrics": sorted(runtime)}


def verify_runtime(path, service, request_span):
    metrics, spans = set(), set()
    for line in path.read_text().splitlines():
        record = json.loads(line)
        for kind, scopes, values, found in (("resourceMetrics", "scopeMetrics", "metrics", metrics),
                                             ("resourceSpans", "scopeSpans", "spans", spans)):
            for resource in record.get(kind, []):
                attrs = {a["key"]: a["value"].get("stringValue") for a in resource["resource"]["attributes"]}
                if attrs.get("qualification.phase") != "full-service" or attrs.get("service.name") != service:
                    continue
                assert attrs.get("service.name") == service, attrs
                assert attrs.get("deployment.environment.name") == "qualification", attrs
                for scope in resource.get(scopes, []):
                    found.update(value["name"] for value in scope.get(values, []))
    assert "go.goroutine.count" in metrics, metrics
    assert request_span in spans, spans
    return {"service": service, "runtime_metric": "go.goroutine.count",
            "request_span": request_span, "healthy_dependencies": True}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--collector", required=True, type=Path)
    parser.add_argument("--full-gateway", action="store_true", help="also boot the actual gateway entrypoint with managed dependencies")
    parser.add_argument("--full-accounts", action="store_true", help="also boot the actual Accounts entrypoint with managed dependencies")
    parser.add_argument("--core", type=Path, help="development-only Core source override")
    parser.add_argument("--sdk", type=Path, help="development-only SDK source override")
    args = parser.parse_args()
    binary = args.collector.resolve(strict=True)
    root = Path(__file__).resolve().parents[2]
    workspace = subprocess.check_output(["go", "env", "GOWORK"], cwd=root, text=True).strip()
    version = subprocess.check_output([str(binary), "--version"], text=True).strip()
    with tempfile.TemporaryDirectory(prefix="service-otlp-") as directory:
        directory = Path(directory)
        ingest, health = address(), address()
        output, config = directory / "metrics.json", directory / "collector.yaml"
        config.write_text(f'''receivers:
  otlp:
    protocols:
      grpc:
        endpoint: "{ingest}"
exporters:
  file:
    path: "{output}"
extensions:
  health_check:
    endpoint: "{health}"
service:
  extensions: [health_check]
  pipelines:
    metrics:
      receivers: [otlp]
      exporters: [file]
    traces:
      receivers: [otlp]
      exporters: [file]
''')
        with (directory / "collector.log").open("w+") as log:
            process = subprocess.Popen([str(binary), "--config", str(config)], stdout=log, stderr=log)
            try:
                wait_ready(process, health)
                for service in ("accounts", "auth-gateway"):
                    env = dict(os.environ)
                    env.update(QUALIFICATION_OTLP_ENDPOINT=f"http://{ingest}", OTEL_SERVICE_NAME=service,
                               OTEL_RESOURCE_ATTRIBUTES="deployment.environment.name=qualification")
                    source = root / "module/services" / service / "code"
                    flags = []
                    if args.core or args.sdk:
                        modfile = directory / f"{service}.mod"
                        shutil.copyfile(source / "go.mod", modfile)
                        shutil.copyfile(source / "go.sum", modfile.with_suffix(".sum"))
                        for module, override in (("core", args.core), ("sdk-go", args.sdk)):
                            if override:
                                subprocess.run(["go", "mod", "edit", f"-modfile={modfile}",
                                                f"-replace=github.com/codefly-dev/{module}={override.resolve(strict=True)}"],
                                               cwd=source, check=True)
                        flags = [f"-modfile={modfile}", "-mod=mod"]
                    subprocess.run(["go", "test", *flags, "-tags", "otel_integration", ".", "-run",
                                    "^TestMetricsToInfrastructureCollector$", "-count=1"],
                                   cwd=source, env=env, check=True)
                    if (args.full_accounts and service == "accounts") or (args.full_gateway and service == "auth-gateway"):
                        subprocess.run(["go", "test", *flags, "-tags", "otel_integration", ".", "-run",
                                        "^TestFullServiceToInfrastructureCollector$", "-count=1", "-timeout=150s"],
                                       cwd=source, env=env, check=True, timeout=180)
            finally:
                process.terminate()
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
                log.seek(0)
                if process.returncode not in (0, -15):
                    raise RuntimeError(log.read())
        result = verify(output)
        if args.full_accounts:
            result["full_service"] = verify_runtime(output, "accounts", "saas.accounts.v1.UserService/Version")
        if args.full_gateway:
            result["full_gateway"] = verify_runtime(output, "auth-gateway", "GET /ready")
        print(json.dumps({"collector": version, "development_overrides": bool(args.core or args.sdk or workspace not in ("", "off")), "go_workspace": workspace, **result}, indent=2))


if __name__ == "__main__":
    main()
