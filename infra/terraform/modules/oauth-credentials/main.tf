# Story 2.3 — OAuth provider credentials (Google + GitHub) for auth-svc.
#
# Wright Round 1 Q5 ruling (APPROVED option a): provider client IDs +
# secrets land as a K8s Secret (KMS-wrapped at rest), sibling to the
# JWT signing-key Secret from Story 2.2 Q3. No Vault dependency in
# this Story — the Vault PKI migration tracker (Story 2.2 + Story 1.6
# M-1) absorbs both JWT keys and OAuth credentials in a future cut-over.
#
# The non-secret `redirect_uri_base` lives in a sibling ConfigMap so an
# operator can swap staging/prod URLs without rotating the Secret.

terraform {
  required_version = ">= 1.6"
  required_providers {
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = ">= 2.27"
    }
  }
}

resource "kubernetes_secret" "he_api_oauth_credentials" {
  metadata {
    name      = "he-api-oauth-credentials"
    namespace = var.namespace
    annotations = {
      "cloud.alibaba.com/kms-encrypted" = "true"
    }
    labels = {
      "he-api/story" = "2.3"
      "he-api/role"  = "oauth-credentials"
    }
  }
  type = "Opaque"
  data = {
    GOOGLE_CLIENT_ID     = var.google_client_id
    GOOGLE_CLIENT_SECRET = var.google_client_secret
    GITHUB_CLIENT_ID     = var.github_client_id
    GITHUB_CLIENT_SECRET = var.github_client_secret
  }
}

resource "kubernetes_config_map" "he_api_oauth_config" {
  metadata {
    name      = "he-api-oauth-config"
    namespace = var.namespace
    labels = {
      "he-api/story" = "2.3"
      "he-api/role"  = "oauth-config"
    }
  }
  data = {
    # Base origin for OAuth callback URL construction. Provider authorize
    # redirects land at `{OAUTH_REDIRECT_URI_BASE}/v1/auth/oauth/{provider}/callback`.
    # Operator overrides per environment (staging vs prod). Non-secret —
    # the value is registered with each OAuth provider's authorized
    # redirect-URI list, which is itself public per RFC 6749 §3.1.2.
    OAUTH_REDIRECT_URI_BASE = var.redirect_uri_base
  }
}
