"""Story 10.2 R-OQ-3 — single-source default endpoint + env-var names.

The default gateway base_url lives here as one named constant so an
infra-driven domain change is a one-line edit (Architect Minor follow-up).
All three values are overridable at construction time or via env (BR-10.2.7).
"""

from __future__ import annotations

# R-OQ-3: default gateway endpoint. `/v1` suffix follows the openai SDK
# "base_url then path" convention. Overridable: ctor > HE_API_BASE_URL > this.
DEFAULT_BASE_URL = "https://api.he-api.com/v1"

# BR-10.2.7: credentials come from HE_API_KEY — never OPENAI_API_KEY (key
# isolation, so a real-OpenAI key sitting in the environment can't cross-talk).
HE_API_KEY_ENV = "HE_API_KEY"
HE_API_BASE_URL_ENV = "HE_API_BASE_URL"
