output "credentials_secret_name" {
  description = "Name of the K8s Secret holding the OAuth client IDs + secrets. Helm `auth-svc` values reference this via `oauth.secretName`."
  value       = kubernetes_secret.he_api_oauth_credentials.metadata[0].name
}

output "config_configmap_name" {
  description = "Name of the K8s ConfigMap holding non-secret OAuth runtime config (OAUTH_REDIRECT_URI_BASE). Helm references via `oauth.configMapName`."
  value       = kubernetes_config_map.he_api_oauth_config.metadata[0].name
}
