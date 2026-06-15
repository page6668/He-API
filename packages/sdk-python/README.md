# he-api — Python SDK

A **drop-in, OpenAI-compatible** Python client for the [He-API](https://he-api.com)
gateway. The gateway speaks the OpenAI wire protocol, so this SDK achieves
interface identity by **inheritance, not rewrite**: `Client` / `AsyncClient`
subclass `openai.OpenAI` / `openai.AsyncOpenAI` and override only credential +
endpoint resolution. Every call surface — `chat`, `embeddings`, `models`,
`audio`, streaming, `with_raw_response` — is inherited unchanged.

## Install

```bash
pip install he-api
```

- **Distribution name:** `he-api` &nbsp;•&nbsp; **import name:** `he_api`
- **Requires:** Python ≥ 3.9 &nbsp;•&nbsp; depends on `openai>=1.40,<2`

```python
import he_api
print(he_api.__version__)
```

## Drop-in migration (zero code change beyond the import)

If you already use the official OpenAI SDK:

```python
# before
from openai import OpenAI
client = OpenAI()                       # base_url -> OpenAI, key <- OPENAI_API_KEY

# after — drop-in
from he_api import Client
client = Client()                       # base_url -> He-API gateway, key <- HE_API_KEY
```

- **`base_url`** defaults to the He-API gateway (`https://api.he-api.com/v1`).
  Override via the `base_url=` argument or the `HE_API_BASE_URL` env var.
- **Credentials** come from the **`HE_API_KEY`** env var (or the `api_key=`
  argument). `OPENAI_API_KEY` is intentionally **never** read, so a real-OpenAI
  key in your environment can't cross-talk.

### Chat, streaming, embeddings, models

```python
from he_api import Client
client = Client()

# non-streaming
resp = client.chat.completions.create(
    model="qwen-max",
    messages=[{"role": "user", "content": "Hi"}],
)

# streaming (+ tail usage)
for chunk in client.chat.completions.create(
    model="qwen-max",
    messages=[{"role": "user", "content": "Hi"}],
    stream=True,
    stream_options={"include_usage": True},
):
    ...

client.embeddings.create(model="text-embedding-v2", input="hello")
client.models.list()
```

### Async

```python
import asyncio
from he_api import AsyncClient

async def main():
    client = AsyncClient()              # mirrors openai.AsyncOpenAI
    resp = await client.chat.completions.create(
        model="qwen-max",
        messages=[{"role": "user", "content": "Hi"}],
    )

asyncio.run(main())
```

## He-API extensions

These all ride on the inherited openai SDK — no special API surface.

```python
# routing strategy / A-B testing — per-request headers (gateway validates)
client.chat.completions.create(
    model="he-router-cost",
    messages=[{"role": "user", "content": "Hi"}],
    extra_headers={"X-He-Routing-Strategy": "cost"},
)

# read He response headers via the inherited raw-response accessor
raw = client.chat.completions.with_raw_response.create(
    model="qwen-max", messages=[{"role": "user", "content": "Hi"}],
)
raw.headers.get("x-he-selected-model")
raw.headers.get("x-he-request-id")
# NOTE: x-he-cost-usd is non-authoritative and may be absent — never bill from it.

# non-OpenAI gateway endpoints (thin convenience wrappers)
client.get_balance(currency="usd")     # GET /v1/balance?currency=usd
client.get_usage()                     # GET /v1/usage
```

## Errors

Errors surface through the **OpenAI exception hierarchy** (re-exported from
`he_api` for convenience), so existing `except openai.BadRequestError` code keeps
working. He-API custom codes and the request id are readable on the exception:

```python
from he_api import Client, BadRequestError
client = Client()
try:
    client.chat.completions.create(model="nope", messages=[{"role": "user", "content": "x"}])
except BadRequestError as e:
    e.code           # "400_invalid_request"
    e.request_id     # he_request_id (req_xxxxxxxxxxxx)
```

## Audio (ASR / TTS) and Vision — auto-inherited

Because the gateway is OpenAI-compatible and `Client` inherits the full openai
surface, **audio and vision work with no extra SDK code** — these are inherited,
not re-implemented:

```python
# ASR (Whisper-compatible)
client.audio.transcriptions.create(model="whisper-1", file=open("a.mp3", "rb"))

# TTS
client.audio.speech.create(model="tts-1", voice="alloy", input="hello")

# Vision — multimodal content parts
client.chat.completions.create(
    model="qwen-vl-max",
    messages=[{"role": "user", "content": [
        {"type": "text", "text": "What is this?"},
        {"type": "image_url", "image_url": {"url": "https://example.com/x.png"}},
    ]}],
)
```

## License

Apache-2.0
