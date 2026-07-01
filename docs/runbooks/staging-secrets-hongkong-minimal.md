# 阶段 4 — 手建 K8s Secret 命令清单(香港 staging · 复制即用)

> 配套 `staging-deploy-hongkong-minimal.md` 阶段 4。Vault/ESO 延后(Epic 9),staging **手建 K8s Secret**。
> 下面每条把 Secret 名 / data key / namespace 都对准了各服务 helm 模板的真实引用 —— 只需把 `<...>` 换成真实值。
> ⚠️ **先决**:namespace 要先存在(`he-api-staging` 由 Terraform 建;`he-api-adapters` 由 ArgoCD `CreateNamespace=true` 建)。
> ⚠️ **不要手建**:`he-api-db-creds`(Terraform 渲染)、`he-api-auth-jwt-keys`(Terraform `tls_private_key` 自动生成)。

---

## 🔴 先决动作:adapter 的 ExternalSecret 开关(不做的话 ArgoCD 同步会失败)

6 家 adapter 的 `externalSecret.enabled` 默认 **`true`**(base values.yaml,为 Epic 9 ESO 预留)。ESO 未装时,它会创建 `ExternalSecret` CR → CRD 不存在 → **同步 admission 失败**。staging 要关掉它(auth-svc 已是 false):

**方式甲(推荐,per-app 覆盖,不动 base):** 给 6 个 `infra/argocd/applications/adapter-*.yaml` 各加:
```yaml
  source:
    helm:
      parameters:
        - name: externalSecret.enabled
          value: "false"
```
**方式乙:** 给 6 个 `infra/helm/adapter-*/values-staging.yaml` 各加:
```yaml
externalSecret:
  enabled: false
```
> 需要我直接把 6 处改好并提交,说一声即可(我建议方式乙,和 image 覆盖放一起最直观)。

---

## A. 模型上游 API Key(namespace `he-api-adapters`,每个 key 名都是 `api_key`)
```bash
kubectl -n he-api-adapters create secret generic adapter-deepseek-upstream-api-key --from-literal=api_key='<DEEPSEEK_KEY>'
kubectl -n he-api-adapters create secret generic adapter-qwen-upstream-api-key     --from-literal=api_key='<QWEN_KEY>'
kubectl -n he-api-adapters create secret generic adapter-kimi-upstream-api-key     --from-literal=api_key='<KIMI_KEY>'
kubectl -n he-api-adapters create secret generic adapter-glm-upstream-api-key      --from-literal=api_key='<GLM_KEY>'
kubectl -n he-api-adapters create secret generic adapter-ernie-upstream-api-key    --from-literal=api_key='<ERNIE_KEY>'
kubectl -n he-api-adapters create secret generic adapter-doubao-upstream-api-key   --from-literal=api_key='<DOUBAO_KEY>'
```
> ⚠️ **已知 GAP**:Doubao 的 **ASR/TTS token**(`DOUBAO_ASR_UPSTREAM_API_TOKEN` / `DOUBAO_TTS_UPSTREAM_API_TOKEN`,代码 main.go 读)**尚未在 helm 里接线**。chat/vision 用上面的 `api_key` 即可;**语音(Story 9.6/9.7)要等这段接线补上**(单独 follow-up,不挡早期上线)。

## B. OAuth 登录(namespace `he-api-staging`,Secret `he-api-oauth-credentials`)
```bash
kubectl -n he-api-staging create secret generic he-api-oauth-credentials \
  --from-literal=GOOGLE_CLIENT_ID='<...>' \
  --from-literal=GOOGLE_CLIENT_SECRET='<...>' \
  --from-literal=GITHUB_CLIENT_ID='<...>' \
  --from-literal=GITHUB_CLIENT_SECRET='<...>'
```

## C. SendGrid 发信(namespace `he-api-staging`,Secret `he-api-notification-creds`)
```bash
kubectl -n he-api-staging create secret generic he-api-notification-creds \
  --from-literal=SENDGRID_API_KEY='<...>'
```

## D. 支付 5 渠道(namespace `he-api-staging`,Secret `he-api-payment-provider`)
> payment-svc 用 `envFrom: secretRef` 整包注入 —— **data key 必须是环境变量名**(下面 18 个,来自 `paymentProvider.requiredKeys` 权威清单)。某渠道不上线就留空该 key,那条通道自动关闭。
```bash
kubectl -n he-api-staging create secret generic he-api-payment-provider \
  --from-literal=STRIPE_SECRET_KEY='<...>' \
  --from-literal=STRIPE_WEBHOOK_SIGNING_SECRET='<...>' \
  --from-literal=PAYPAL_CLIENT_ID='<...>' \
  --from-literal=PAYPAL_CLIENT_SECRET='<...>' \
  --from-literal=PAYPAL_WEBHOOK_ID='<...>' \
  --from-literal=COINBASE_COMMERCE_API_KEY='<...>' \
  --from-literal=COINBASE_COMMERCE_WEBHOOK_SECRET='<...>' \
  --from-literal=ALIPAY_PLUS_CLIENT_ID='<...>' \
  --from-literal=ALIPAY_PLUS_MERCHANT_PRIVATE_KEY='<...>' \
  --from-literal=ALIPAY_PLUS_ALIPAY_PUBLIC_KEY='<...>' \
  --from-literal=WECHAT_PAY_MCH_ID='<...>' \
  --from-literal=WECHAT_PAY_MERCHANT_PRIVATE_KEY='<...>' \
  --from-literal=WECHAT_PAY_MERCHANT_CERT_SERIAL='<...>' \
  --from-literal=WECHAT_PAY_PLATFORM_PUBLIC_KEY='<...>' \
  --from-literal=WECHAT_PAY_PLATFORM_CERT_SERIAL='<...>' \
  --from-literal=WECHAT_PAY_APIV3_KEY='<...>'
```
> 密钥/证书类值(私钥、公钥)建议用 `--from-file=WECHAT_PAY_MERCHANT_PRIVATE_KEY=./mch_private_key.pem` 从文件读,避免命令行转义问题。

## E. 汇率 provider(namespace `he-api-staging`,Secret `he-api-fxrate-provider`)
```bash
kubectl -n he-api-staging create secret generic he-api-fxrate-provider \
  --from-literal=HE_API_FX_PROVIDER_URL='<...>'
```
> 详见 `docs/dev/secrets/fxrate-provider.md`;若该 provider 还需 API key,按该文档补对应 key。

---

## 建完自检
```bash
kubectl -n he-api-adapters get secret | grep upstream-api-key      # 应有 6 个
kubectl -n he-api-staging  get secret he-api-oauth-credentials he-api-notification-creds he-api-payment-provider he-api-fxrate-provider
```
- [ ] 6 adapter + OAuth + SendGrid + payment + fxrate 都在
- [ ] payment 那个 `kubectl -n he-api-staging get secret he-api-payment-provider -o jsonpath='{.data}' | ...` 确认 16 个 key 齐(未上线渠道可留空)
- [ ] 已处理顶部 🔴 ExternalSecret 开关

## 出处(权威来源,非我杜撰)
- adapter secret 名/key:`infra/helm/adapter-*/templates/deployment.yaml`(`secretKeyRef`)
- OAuth:`infra/helm/auth-svc/values.yaml`(`oauth.secretName`)
- SendGrid:`infra/helm/notification-svc/values.yaml`(`sendgrid.credsSecretName`)
- 支付 18 key:`infra/helm/payment-svc/values.yaml`(`paymentProvider.requiredKeys`)
- 汇率:`infra/helm/billing-svc/values.yaml`(`fx.providerSecretName`)
