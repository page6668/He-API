"""Story 10.2: Python SDK (drop-in OpenAI 替代) — implemented test suite.

Originally an auto-generated skeleton (QA Test Design, Turing, 2026-06-15); every
stubbed body has now been implemented by Dev (Linus). Each test maps to a
designed scenario id (10.2-UNIT/INT/DOC/E2E/BLIND-*). No test was deleted.

Test Design: docs/qa/assessments/10.2-test-design-20260615.md

Conventions (ratified):
  - Client subclasses openai.OpenAI, AsyncClient subclasses openai.AsyncOpenAI (R-OQ-1).
  - Hermetic transport mocking via `respx` over httpx — NO real gateway (R-OQ-5).
  - Reuse the gateway drop-in oracle shapes from
    apps/api-gateway/tests/_protocol_invariants.py (made importable in conftest.py).
  - DEFAULT_BASE_URL == "https://api.he-api.com/v1" (R-OQ-3).
  - Credentials from HE_API_KEY; OPENAI_API_KEY MUST NOT be read (BR-10.2.7).
"""

from __future__ import annotations

import json
import subprocess
import sys
import time
import zipfile
from pathlib import Path

import httpx
import openai
import pytest
import respx

# Drop-in oracle: the SAME shape assertions the gateway contract tests use.
from _protocol_invariants import (  # noqa: E402  (path injected by conftest)
    REQUEST_ID_RE,
    assert_chat_completion_chunk_shape,
    assert_chat_completion_shape,
    assert_embedding_shape,
    assert_error_envelope_shape,
    assert_model_entry_shape,
)

import he_api
from he_api import AsyncClient, Client

GATEWAY = "https://api.he-api.com/v1"
_PKG_ROOT = Path(__file__).resolve().parents[1]
_HE_RID = "req_0123456789ab"  # matches REQUEST_ID_RE ^req_[0-9a-f]{12}$


# --- canonical OpenAI-shaped response builders (gateway-faithful) ---------------


def _base_url_of(client) -> str:
    """Normalised base_url string (openai appends a trailing slash)."""
    return str(client.base_url).rstrip("/")


def chat_completion_body(model: str = "qwen-max") -> dict:
    return {
        "id": "chatcmpl-abc123",
        "object": "chat.completion",
        "created": int(time.time()),
        "model": model,
        "choices": [
            {
                "index": 0,
                "message": {"role": "assistant", "content": "Hello there!"},
                "finish_reason": "stop",
            }
        ],
        "usage": {"prompt_tokens": 9, "completion_tokens": 12, "total_tokens": 21},
    }


def sse_stream(model: str = "qwen-max", *, include_usage: bool = False) -> bytes:
    """OpenAI SSE: bootstrap(role) → content → terminal(finish_reason[, usage]) → [DONE]."""
    cid, created = "chatcmpl-abc123", int(time.time())

    def frame(delta: dict, finish_reason=None, extra: dict | None = None) -> str:
        obj = {
            "id": cid,
            "object": "chat.completion.chunk",
            "created": created,
            "model": model,
            "choices": [{"index": 0, "delta": delta, "finish_reason": finish_reason}],
        }
        if extra:
            obj.update(extra)
        return "data: " + json.dumps(obj) + "\n\n"

    parts = [frame({"role": "assistant"}), frame({"content": "Hello"})]
    # MED-5: gateway carries usage on the terminal (finish_reason) chunk when
    # include_usage=True; otherwise usage is absent there.
    term_extra = (
        {"usage": {"prompt_tokens": 9, "completion_tokens": 3, "total_tokens": 12}}
        if include_usage
        else None
    )
    parts.append(frame({}, finish_reason="stop", extra=term_extra))
    parts.append("data: [DONE]\n\n")
    return "".join(parts).encode()


def embeddings_body(model: str = "text-embedding-v2") -> dict:
    return {
        "object": "list",
        "data": [{"object": "embedding", "index": 0, "embedding": [0.1, 0.2, 0.3]}],
        "model": model,
        "usage": {"prompt_tokens": 4, "total_tokens": 4},
    }


def models_body() -> dict:
    return {
        "object": "list",
        "data": [
            {
                "id": "qwen-max",
                "object": "model",
                "created": int(time.time()),
                "owned_by": "he-api",
                "capabilities": {
                    "chat": True,
                    "streaming": True,
                    "vision": False,
                    "embeddings": False,
                    "audio": False,
                    "tools": True,
                    "json_mode": True,
                },
            }
        ],
    }


def error_envelope(code: str, error_type: str = "invalid_request_error") -> dict:
    return {
        "error": {
            "code": code,
            "message": "synthetic gateway error",
            "type": error_type,
            "param": None,
            "he_request_id": _HE_RID,
        }
    }


def _sse_response(content: bytes) -> httpx.Response:
    return httpx.Response(200, headers={"content-type": "text/event-stream"}, content=content)


def _classify_chunk(chunk):
    """Map a streamed chunk to assert_*_chunk_shape's (is_bootstrap, is_terminal)."""
    choice = chunk.choices[0]
    delta = choice.delta
    role = getattr(delta, "role", None)
    content = getattr(delta, "content", None)
    fr = choice.finish_reason
    return {
        "is_bootstrap": bool(role) and not content and fr is None,
        "is_terminal": fr is not None,
    }


# Build the distribution ONCE for the AC1 packaging integration tests.
@pytest.fixture(scope="module")
def built_dist(tmp_path_factory) -> dict:
    """`python -m build --no-isolation` → {dist_dir, wheel, sdist}.

    --no-isolation reuses the already-installed hatchling so the build stays
    network-free and hermetic for CI.
    """
    out = tmp_path_factory.mktemp("dist")
    proc = subprocess.run(
        [sys.executable, "-m", "build", "--no-isolation", "--outdir", str(out), str(_PKG_ROOT)],
        capture_output=True,
        text=True,
    )
    assert proc.returncode == 0, f"build failed:\nSTDOUT:{proc.stdout}\nSTDERR:{proc.stderr}"
    wheels = list(out.glob("*.whl"))
    sdists = list(out.glob("*.tar.gz"))
    assert wheels, f"no wheel produced; build output:\n{proc.stdout}"
    assert sdists, f"no sdist produced; build output:\n{proc.stdout}"
    return {"dir": out, "wheel": wheels[0], "sdist": sdists[0]}


# ============================================================
# AC1: `pip install he-api` 可用 — publishable, importable package
# ============================================================


class TestAC1Packaging:
    # --- P0 ---

    def test_10_2_unit_001_import_entry(self):
        # Pri: P0 | Level: unit
        # `import he_api`; `from he_api import Client, AsyncClient`; Client() constructs
        # with no He-API private source (only the public openai dep + an api key).
        assert issubclass(Client, openai.OpenAI)
        assert issubclass(AsyncClient, openai.AsyncOpenAI)
        client = Client(api_key="he-test-key")
        assert isinstance(client, openai.OpenAI)
        # Constructed purely from public surface — nothing under he_api imports a
        # private/internal He module beyond the openai SDK + stdlib + httpx.
        assert _base_url_of(client) == GATEWAY

    def test_10_2_unit_002_version_single_source(self):
        # Pri: P0 | Level: unit | BR-10.2.4 / R-OQ-6
        from importlib.metadata import version

        assert he_api.__version__ == version("he-api"), "version drift: __version__ vs metadata"

    def test_10_2_unit_003_pin_drift_guard_openai(self):
        # Pri: P0 | Level: unit | BR-10.2.9 / R-OQ-1 (mirrors gateway openai==1.40.*)
        # FAIL LOUDLY if the installed openai escapes >=1.40,<2 (e.g. a 2.x leaks in).
        parts = openai.__version__.split(".")
        major, minor = int(parts[0]), int(parts[1])
        assert (major, minor) >= (1, 40), (
            f"openai {openai.__version__} below floor 1.40 — drop-in parsing may regress"
        )
        assert major < 2, (
            f"openai {openai.__version__} crossed the major-2 breaking boundary — "
            "re-run the gateway contract suite and bump the pin deliberately"
        )

    def test_10_2_int_001_build_and_clean_install(self, built_dist, tmp_path):
        # Pri: P0 | Level: integration | BR-10.2.1/.3
        # Install the BUILT wheel (not the editable source) into a fresh target and
        # import it in a subprocess — proves the artifact is self-contained.
        target = tmp_path / "site"
        inst = subprocess.run(
            [sys.executable, "-m", "pip", "install", "--no-deps", "--target", str(target),
             str(built_dist["wheel"])],
            capture_output=True, text=True,
        )
        assert inst.returncode == 0, f"wheel install failed:\n{inst.stderr}"
        # `target` first on the path so the built wheel wins over the editable install;
        # openai resolves from the surrounding venv.
        check = subprocess.run(
            [sys.executable, "-c", "import he_api; print(he_api.__version__)"],
            capture_output=True, text=True,
            env={**__import__("os").environ, "PYTHONPATH": str(target)},
        )
        assert check.returncode == 0, f"import of built wheel failed:\n{check.stderr}"
        assert check.stdout.strip() == he_api.__version__

    def test_10_2_int_002_twine_check_metadata(self, built_dist):
        # Pri: P0 | Level: integration | BR-10.2.3 / R-OQ-2
        proc = subprocess.run(
            [sys.executable, "-m", "twine", "check",
             str(built_dist["wheel"]), str(built_dist["sdist"])],
            capture_output=True, text=True,
        )
        assert proc.returncode == 0, f"twine check failed:\n{proc.stdout}\n{proc.stderr}"
        assert "PASSED" in proc.stdout, f"twine check did not PASS:\n{proc.stdout}"

    def test_10_2_int_003_dist_vs_import_name_split(self, built_dist):
        # Pri: P0 | Level: integration | BR-10.2.2
        # distribution name == "he-api"; import package dir == "he_api"
        import tomllib

        pyproject = tomllib.loads((_PKG_ROOT / "pyproject.toml").read_text())
        assert pyproject["project"]["name"] == "he-api"
        assert (_PKG_ROOT / "he_api" / "__init__.py").is_file()
        # The built wheel carries the import package `he_api/`, distribution `he_api-*`.
        with zipfile.ZipFile(built_dist["wheel"]) as zf:
            names = zf.namelist()
        assert any(n.startswith("he_api/") for n in names), names
        assert built_dist["wheel"].name.startswith("he_api-")

    # --- P1 ---

    def test_10_2_unit_004_requires_python_floor(self):
        # Pri: P1 | Level: unit | R-OQ-6
        from importlib.metadata import metadata

        assert metadata("he-api")["Requires-Python"] == ">=3.9"

    def test_10_2_unit_005_runtime_openai_range_not_tight(self):
        # Pri: P1 | Level: unit | R-OQ-1 (layered pin)
        from importlib.metadata import requires

        deps = requires("he-api") or []
        openai_reqs = [d for d in deps if d.split(";")[0].strip().lower().startswith("openai")]
        assert openai_reqs, f"no openai runtime dep declared; got {deps}"
        # importlib normalises specifier ordering, so assert on content not exact string:
        # a COMPATIBLE RANGE (>=1.40,<2), never a tight `==` pin (that lives in test deps).
        spec = openai_reqs[0].replace(" ", "")
        assert ">=1.40" in spec and "<2" in spec, f"runtime openai must be a range; got {spec!r}"
        assert "==" not in spec, f"runtime pin must NOT be tight; got {spec!r}"

    def test_10_2_unit_006_build_backend_and_metadata(self):
        # Pri: P1 | Level: unit | R-OQ-6 / BR-10.2.3
        import tomllib

        pyproject = tomllib.loads((_PKG_ROOT / "pyproject.toml").read_text())
        assert pyproject["build-system"]["build-backend"] == "hatchling.build"
        urls = {k.lower(): v for k, v in pyproject["project"]["urls"].items()}
        assert "homepage" in urls and "documentation" in urls and "repository" in urls, urls

    # --- P2 ---

    def test_10_2_doc_001_readme_install_and_dropin(self):
        # Pri: P2 | Level: doc-gate
        readme = (_PKG_ROOT / "README.md").read_text()
        assert "pip install he-api" in readme
        assert "import he_api" in readme
        assert "from openai import OpenAI" in readme
        assert "from he_api import Client" in readme


# ============================================================
# AC2: 与 OpenAI 官方 Python SDK 接口一致 — drop-in interface identity
# ============================================================


class TestAC2DropIn:
    # --- P0 (drop-in core) ---

    def test_10_2_unit_010_default_base_url(self):
        # Pri: P0 | Level: unit | R-OQ-3
        with respx.mock as rsx:
            route = rsx.post(f"{GATEWAY}/chat/completions").mock(
                return_value=httpx.Response(200, json=chat_completion_body())
            )
            client = Client(api_key="k")  # no base_url
            assert _base_url_of(client) == GATEWAY
            client.chat.completions.create(
                model="qwen-max", messages=[{"role": "user", "content": "x"}]
            )
            assert str(route.calls.last.request.url) == f"{GATEWAY}/chat/completions"

    def test_10_2_unit_011_base_url_precedence(self, monkeypatch):
        # Pri: P0 | Level: unit | BR-10.2.7  (ctor > HE_API_BASE_URL > default)
        monkeypatch.setenv("HE_API_BASE_URL", "https://env.example/v1")
        assert _base_url_of(Client(api_key="k", base_url="https://ctor.example/v1")) == "https://ctor.example/v1"
        assert _base_url_of(Client(api_key="k")) == "https://env.example/v1"
        monkeypatch.delenv("HE_API_BASE_URL")
        assert _base_url_of(Client(api_key="k")) == GATEWAY

    def test_10_2_unit_012_api_key_isolation(self, monkeypatch):
        # Pri: P0 | Level: unit | BR-10.2.7
        # OPENAI_API_KEY set, HE_API_KEY unset → NOT read; native missing-key error.
        monkeypatch.setenv("OPENAI_API_KEY", "sk-must-not-be-used")
        with pytest.raises(openai.OpenAIError):
            Client()
        # And HE_API_KEY IS honoured (no cross-talk).
        monkeypatch.setenv("HE_API_KEY", "he-key")
        assert Client().api_key == "he-key"

    def test_10_2_int_010_chat_non_stream_shape(self):
        # Pri: P0 | Level: integration | mirror assert_chat_completion_shape
        with respx.mock as rsx:
            route = rsx.post(f"{GATEWAY}/chat/completions").mock(
                return_value=httpx.Response(200, json=chat_completion_body("qwen-max"))
            )
            resp = Client(api_key="k").chat.completions.create(
                model="qwen-max", messages=[{"role": "user", "content": "Hi"}]
            )
            assert_chat_completion_shape(resp, "qwen-max")
            assert str(route.calls.last.request.url).startswith(f"{GATEWAY}/chat/completions")

    def test_10_2_int_011_chat_stream_shape(self):
        # Pri: P0 | Level: integration | mirror assert_chat_completion_chunk_shape
        with respx.mock as rsx:
            rsx.post(f"{GATEWAY}/chat/completions").mock(return_value=_sse_response(sse_stream()))
            stream = Client(api_key="k").chat.completions.create(
                model="qwen-max", messages=[{"role": "user", "content": "Hi"}], stream=True
            )
            chunks = list(stream)
            assert len(chunks) == 3  # bootstrap + content + terminal; [DONE] consumed
            for chunk in chunks:
                assert_chat_completion_chunk_shape(chunk, "qwen-max", **_classify_chunk(chunk))

    def test_10_2_int_012_stream_usage_sum_invariant(self):
        # Pri: P0 | Level: integration
        with respx.mock as rsx:
            rsx.post(f"{GATEWAY}/chat/completions").mock(
                return_value=_sse_response(sse_stream(include_usage=True))
            )
            stream = Client(api_key="k").chat.completions.create(
                model="qwen-max",
                messages=[{"role": "user", "content": "Hi"}],
                stream=True,
                stream_options={"include_usage": True},
            )
            usages = [c.usage for c in stream if c.usage is not None]
            assert usages, "no chunk carried usage despite include_usage=True"
            tail = usages[-1]
            assert tail.prompt_tokens + tail.completion_tokens == tail.total_tokens

    def test_10_2_int_013_embeddings_shape(self):
        # Pri: P0 | Level: integration | mirror assert_embedding_shape
        with respx.mock as rsx:
            rsx.post(f"{GATEWAY}/embeddings").mock(
                return_value=httpx.Response(200, json=embeddings_body("text-embedding-v2"))
            )
            resp = Client(api_key="k").embeddings.create(model="text-embedding-v2", input="hello")
            assert_embedding_shape(resp, "text-embedding-v2")

    def test_10_2_int_014_models_list_shape(self):
        # Pri: P0 | Level: integration | mirror assert_model_entry_shape
        with respx.mock as rsx:
            rsx.get(f"{GATEWAY}/models").mock(return_value=httpx.Response(200, json=models_body()))
            page = Client(api_key="k").models.list()
            entries = list(page)
            assert entries
            for entry in entries:
                assert_model_entry_shape(entry)

    def test_10_2_int_015_error_400_passthrough(self):
        # Pri: P0 | Level: integration | mirror assert_error_envelope_shape | R-OQ-4d
        body = error_envelope("400_invalid_request")
        with respx.mock as rsx:
            rsx.post(f"{GATEWAY}/chat/completions").mock(
                return_value=httpx.Response(400, json=body, headers={"x-request-id": _HE_RID})
            )
            with pytest.raises(openai.BadRequestError) as exc:
                Client(api_key="k").chat.completions.create(
                    model="nope", messages=[{"role": "user", "content": "x"}]
                )
            err = exc.value
            assert err.code == "400_invalid_request"
            assert err.request_id == _HE_RID and REQUEST_ID_RE.match(err.request_id)
            # body's he_request_id is readable and well-formed
            assert err.response.json()["error"]["he_request_id"] == _HE_RID
            assert_error_envelope_shape(body, "400_invalid_request", 400)

    def test_10_2_int_016_error_402_quota_exhausted(self):
        # Pri: P0 | Level: integration
        with respx.mock as rsx:
            rsx.post(f"{GATEWAY}/chat/completions").mock(
                return_value=httpx.Response(402, json=error_envelope("402_quota_exhausted"))
            )
            with pytest.raises(openai.APIStatusError) as exc:
                Client(api_key="k").chat.completions.create(
                    model="qwen-max", messages=[{"role": "user", "content": "x"}]
                )
            err = exc.value
            assert err.status_code == 402
            assert err.code == "402_quota_exhausted"  # not swallowed

    @pytest.mark.asyncio
    async def test_10_2_int_017_async_parity(self):
        # Pri: P0 | Level: integration | BR-10.2.6 (Python-specific, no TS analog)
        with respx.mock as rsx:
            rsx.post(f"{GATEWAY}/chat/completions").mock(
                side_effect=[
                    httpx.Response(200, json=chat_completion_body("qwen-max")),
                    _sse_response(sse_stream()),
                ]
            )
            client = AsyncClient(api_key="k")
            assert _base_url_of(client) == GATEWAY  # same resolution as sync Client
            assert client.api_key == "k"
            resp = await client.chat.completions.create(
                model="qwen-max", messages=[{"role": "user", "content": "Hi"}]
            )
            assert_chat_completion_shape(resp, "qwen-max")
            stream = await client.chat.completions.create(
                model="qwen-max", messages=[{"role": "user", "content": "Hi"}], stream=True
            )
            chunks = [chunk async for chunk in stream]
            assert len(chunks) == 3
            for chunk in chunks:
                assert_chat_completion_chunk_shape(chunk, "qwen-max", **_classify_chunk(chunk))
            await client.close()

    # --- P1 (He extensions — ratified MVP cut-line, R-OQ-4) ---

    def test_10_2_unit_013_type_exception_reexport(self):
        # Pri: P1 | Level: unit | BR-10.2.8
        # he_api re-exports the openai exception classes (identity), so both
        # `except he_api.X` and `except openai.X` catch the same instances.
        assert he_api.BadRequestError is openai.BadRequestError
        assert he_api.OpenAIError is openai.OpenAIError
        assert he_api.RateLimitError is openai.RateLimitError
        with respx.mock as rsx:
            rsx.post(f"{GATEWAY}/chat/completions").mock(
                return_value=httpx.Response(400, json=error_envelope("400_invalid_request"))
            )
            with pytest.raises(openai.BadRequestError):  # holds for he_api-raised error
                Client(api_key="k").chat.completions.create(
                    model="m", messages=[{"role": "user", "content": "x"}]
                )

    def test_10_2_int_020_routing_header_passthrough(self):
        # Pri: P1 | Level: integration | R-OQ-4b
        with respx.mock as rsx:
            route = rsx.post(f"{GATEWAY}/chat/completions").mock(
                return_value=httpx.Response(200, json=chat_completion_body())
            )
            Client(api_key="k").chat.completions.create(
                model="he-router-cost",
                messages=[{"role": "user", "content": "x"}],
                extra_headers={"X-He-Routing-Strategy": "cost"},
            )
            # header on the wire, unchanged, not re-validated by the SDK
            assert route.calls.last.request.headers.get("x-he-routing-strategy") == "cost"

    def test_10_2_int_021_ab_models_header_passthrough(self):
        # Pri: P1 | Level: integration | R-OQ-4b
        with respx.mock as rsx:
            route = rsx.post(f"{GATEWAY}/chat/completions").mock(
                return_value=httpx.Response(200, json=chat_completion_body())
            )
            Client(api_key="k").chat.completions.create(
                model="he-router-ab",
                messages=[{"role": "user", "content": "x"}],
                extra_headers={"X-He-AB-Models": "qwen-max,deepseek-v3"},
            )
            assert route.calls.last.request.headers.get("x-he-ab-models") == "qwen-max,deepseek-v3"

    def test_10_2_int_022_with_raw_response_he_headers(self):
        # Pri: P1 | Level: integration | R-OQ-4d
        with respx.mock as rsx:
            rsx.post(f"{GATEWAY}/chat/completions").mock(
                return_value=httpx.Response(
                    200,
                    json=chat_completion_body(),
                    headers={"x-he-request-id": _HE_RID, "x-he-selected-model": "qwen-max"},
                )
            )
            raw = Client(api_key="k").chat.completions.with_raw_response.create(
                model="qwen-max", messages=[{"role": "user", "content": "x"}]
            )
            assert raw.headers.get("x-he-request-id") == _HE_RID
            assert raw.headers.get("x-he-selected-model") == "qwen-max"
            # parse() still yields the OpenAI-shaped object
            assert_chat_completion_shape(raw.parse(), "qwen-max")

    def test_10_2_int_023_cost_usd_absence_tolerant(self):
        # Pri: P1 | Level: integration | OQ5 (project_cost_source_oq5_usage_ledger)
        with respx.mock as rsx:
            rsx.post(f"{GATEWAY}/chat/completions").mock(
                return_value=httpx.Response(
                    200, json=chat_completion_body(), headers={"x-he-request-id": _HE_RID}
                )
            )
            raw = Client(api_key="k").chat.completions.with_raw_response.create(
                model="qwen-max", messages=[{"role": "user", "content": "x"}]
            )
            # cost header deliberately absent — never assume presence / never bill from it
            assert raw.headers.get("x-he-cost-usd") is None

    def test_10_2_int_024_balance_convenience(self):
        # Pri: P1 | Level: integration | R-OQ-4c (only real new code in T3)
        with respx.mock as rsx:
            route = rsx.get(f"{GATEWAY}/balance").mock(
                return_value=httpx.Response(200, json={"currency": "usd", "balance": "12.34"})
            )
            data = Client(api_key="k").get_balance(currency="usd")
            assert data == {"currency": "usd", "balance": "12.34"}
            assert str(route.calls.last.request.url) == f"{GATEWAY}/balance?currency=usd"

    def test_10_2_int_025_usage_convenience(self):
        # Pri: P1 | Level: integration | R-OQ-4c
        with respx.mock as rsx:
            route = rsx.get(f"{GATEWAY}/usage").mock(
                return_value=httpx.Response(200, json={"object": "usage", "total_tokens": 42})
            )
            data = Client(api_key="k").get_usage(params={"granularity": "day"})
            assert data["total_tokens"] == 42
            assert str(route.calls.last.request.url) == f"{GATEWAY}/usage?granularity=day"

    @pytest.mark.skipif(
        "not __import__('os').environ.get('HE_API_TEST_GATEWAY_URL')",
        reason="live drop-in E2E — set HE_API_TEST_GATEWAY_URL to run (skip-if-unset)",
    )
    def test_10_2_e2e_001_live_dropin_equivalence(self):
        # Pri: P1 | Level: e2e (live-gated) | T4.2/T5.2
        import os

        client = Client(
            api_key=os.environ.get("HE_API_TEST_API_KEY", "he-test-key-stub"),
            base_url=os.environ["HE_API_TEST_GATEWAY_URL"].rstrip("/") + "/v1",
        )
        resp = client.chat.completions.create(
            model="qwen-max", messages=[{"role": "user", "content": "Say hi."}]
        )
        assert_chat_completion_shape(resp, "qwen-max")

    # --- P2 (auto-inherited — docs + one smoke each, NO new impl, R-OQ-4) ---

    def test_10_2_int_030_audio_transcriptions_reachable(self):
        # Pri: P2 | Level: integration | BR-10.2.11 (9.6 ASR)
        with respx.mock as rsx:
            route = rsx.post(f"{GATEWAY}/audio/transcriptions").mock(
                return_value=httpx.Response(200, json={"text": "hello world"})
            )
            resp = Client(api_key="k").audio.transcriptions.create(
                model="whisper-1", file=("a.mp3", b"\x00\x00", "audio/mpeg")
            )
            assert resp.text == "hello world"
            assert route.called

    def test_10_2_int_031_audio_speech_reachable(self):
        # Pri: P2 | Level: integration | BR-10.2.11 (9.7 TTS)
        with respx.mock as rsx:
            route = rsx.post(f"{GATEWAY}/audio/speech").mock(
                return_value=httpx.Response(
                    200, content=b"AUDIOBYTES", headers={"content-type": "audio/mpeg"}
                )
            )
            resp = Client(api_key="k").audio.speech.create(model="tts-1", voice="alloy", input="hi")
            assert resp.content == b"AUDIOBYTES"
            assert route.called

    def test_10_2_int_032_multimodal_passthrough(self):
        # Pri: P2 | Level: integration | BR-10.2.11 (9.5 vision)
        with respx.mock as rsx:
            route = rsx.post(f"{GATEWAY}/chat/completions").mock(
                return_value=httpx.Response(200, json=chat_completion_body("qwen-vl-max"))
            )
            resp = Client(api_key="k").chat.completions.create(
                model="qwen-vl-max",
                messages=[
                    {
                        "role": "user",
                        "content": [
                            {"type": "text", "text": "What is this?"},
                            {"type": "image_url", "image_url": {"url": "https://example.com/x.png"}},
                        ],
                    }
                ],
            )
            assert_chat_completion_shape(resp, "qwen-vl-max")
            sent = json.loads(route.calls.last.request.content)
            assert sent["messages"][0]["content"][1]["type"] == "image_url"

    def test_10_2_doc_002_readme_audio_vision_quickstart(self):
        # Pri: P2 | Level: doc-gate | T3.4
        readme = (_PKG_ROOT / "README.md").read_text()
        assert "audio.transcriptions" in readme or "audio.speech" in readme
        assert "image_url" in readme
        assert "auto-inherited" in readme.lower() or "inherited" in readme.lower()


# ============================================================
# Blind Spot Scenarios [BLIND-SPOT]
# library → BOUNDARY, ERROR, RESOURCE (high); CONCURRENCY (medium)
# ============================================================


class TestBlindSpots:
    def test_10_2_blind_boundary_001_missing_key(self):
        # [BLIND-SPOT] BOUNDARY-001 | Pri: P1 | Level: unit
        # neither api_key arg nor HE_API_KEY env → openai-native missing-key error
        with pytest.raises(openai.OpenAIError):
            Client()

    def test_10_2_blind_boundary_002_empty_key(self, monkeypatch):
        # [BLIND-SPOT] BOUNDARY-001/002 | Pri: P1 | Level: unit
        # empty explicit key and empty HE_API_KEY both treated as missing (no bare Bearer)
        with pytest.raises(openai.OpenAIError):
            Client(api_key="")
        monkeypatch.setenv("HE_API_KEY", "")
        with pytest.raises(openai.OpenAIError):
            Client()

    def test_10_2_blind_boundary_003_base_url_path_join(self):
        # [BLIND-SPOT] BOUNDARY-005 | Pri: P1 | Level: integration
        # base_url with trailing slash → no double-slash, /v1 segment preserved
        with respx.mock as rsx:
            route = rsx.post(f"{GATEWAY}/chat/completions").mock(
                return_value=httpx.Response(200, json=chat_completion_body())
            )
            Client(api_key="k", base_url="https://api.he-api.com/v1/").chat.completions.create(
                model="qwen-max", messages=[{"role": "user", "content": "x"}]
            )
            url = str(route.calls.last.request.url)
            assert url == f"{GATEWAY}/chat/completions"
            assert "//chat" not in url.split("://", 1)[1]  # no double slash in the path
            assert "/v1/chat/completions" in url

    def test_10_2_blind_error_001_connection_refused(self):
        # [BLIND-SPOT] ERROR-001 | Pri: P1 | Level: integration
        with respx.mock as rsx:
            rsx.post(f"{GATEWAY}/chat/completions").mock(
                side_effect=httpx.ConnectError("connection refused")
            )
            with pytest.raises(openai.APIConnectionError):
                Client(api_key="k", max_retries=0).chat.completions.create(
                    model="qwen-max", messages=[{"role": "user", "content": "x"}]
                )

    def test_10_2_blind_error_002_malformed_sse(self):
        # [BLIND-SPOT] ERROR-003 | Pri: P1 | Level: integration
        # invalid JSON mid-stream → error surfaced to the consumer, no hang
        bad = b'data: {"id":"chatcmpl-x","object":"chat.completion.chunk"\n\ndata: [DONE]\n\n'
        with respx.mock as rsx:
            rsx.post(f"{GATEWAY}/chat/completions").mock(return_value=_sse_response(bad))
            stream = Client(api_key="k").chat.completions.create(
                model="qwen-max", messages=[{"role": "user", "content": "x"}], stream=True
            )
            with pytest.raises((openai.APIError, ValueError, json.JSONDecodeError)):
                list(stream)

    def test_10_2_blind_error_003_upstream_5xx(self):
        # [BLIND-SPOT] ERROR-002 | Pri: P1 | Level: integration
        with respx.mock as rsx:
            rsx.post(f"{GATEWAY}/chat/completions").mock(
                return_value=httpx.Response(
                    503, json=error_envelope("503_upstream_unavailable", "server_error")
                )
            )
            with pytest.raises(openai.APIStatusError) as exc:
                Client(api_key="k", max_retries=0).chat.completions.create(
                    model="qwen-max", messages=[{"role": "user", "content": "x"}]
                )
            assert exc.value.status_code == 503

    @pytest.mark.asyncio
    async def test_10_2_blind_concurrency_001_header_isolation(self):
        # [BLIND-SPOT] CONCURRENCY-002 | Pri: P1 | Level: integration
        # two concurrent async creates with different extra_headers must not bleed
        import asyncio

        with respx.mock as rsx:
            route = rsx.post(f"{GATEWAY}/chat/completions").mock(
                return_value=httpx.Response(200, json=chat_completion_body())
            )
            client = AsyncClient(api_key="k")

            async def call(strategy: str):
                await client.chat.completions.create(
                    model="he-router",
                    messages=[{"role": "user", "content": "x"}],
                    extra_headers={"X-He-Routing-Strategy": strategy},
                )

            await asyncio.gather(call("cost"), call("latency"))
            seen = sorted(c.request.headers.get("x-he-routing-strategy") for c in route.calls)
            assert seen == ["cost", "latency"]  # each request carried exactly its own value
            await client.close()

    def test_10_2_blind_resource_001_stream_cleanup(self):
        # [BLIND-SPOT] RESOURCE-001 | Pri: P2 | Level: integration
        # break out of a streaming iterator + context-manager exit → clean release
        with respx.mock as rsx:
            rsx.post(f"{GATEWAY}/chat/completions").mock(return_value=_sse_response(sse_stream()))
            with Client(api_key="k") as client:
                stream = client.chat.completions.create(
                    model="qwen-max", messages=[{"role": "user", "content": "x"}], stream=True
                )
                for _ in stream:
                    break  # early exit
                stream.close()  # inherited cleanup must not raise
            assert client.is_closed()
