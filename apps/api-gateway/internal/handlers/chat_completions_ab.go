// Story 6.4 — A/B mode dual-leg dispatch + merge (X-He-AB-Models).
//
// The handler forks here when routing-svc returns is_ab_test=true (BR2-1). Both
// legs are dispatched in PARALLEL via sync.WaitGroup (Architect High-1 — NOT
// errgroup, which is absent from apps/api-gateway/go.mod), each through the
// non-writing dispatchAdapterOnce primitive (Q-G), then mergeABResponses builds
// ONE OpenAI chat.completion carrying both legs (Q-A Option A). Legs are
// concrete pins that do NOT failover (Q-D) and fail independently (Q-E). BOTH
// successful legs are billed (Q-F dual-billing INVARIANT). The fan-out lives
// INSIDE the handler, after the Story-5.3 rate-limit middleware, so one A/B
// request is ONE QPS/RPM tick (Q-J).
package handlers

import (
	"context"
	"net/http"
	"strings"

	"github.com/he-api/he-api/apps/api-gateway/internal/adapterclient"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware/keypolicy"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware/requestid"
	"github.com/he-api/he-api/apps/api-gateway/internal/openaierr"
	"github.com/he-api/he-api/apps/api-gateway/internal/routingclient"

	"log/slog"
	"sync"
)

// abLegResult is one A/B leg's outcome slot. Each leg writes ONLY its own slot
// (index-addressed), so the parallel dispatch needs no shared mutable state and
// is -race clean (BR2-3).
type abLegResult struct {
	model string
	resp  *ChatResponse
	err   error
}

// dispatchAB runs the Story-6.4 A/B path (AC2/AC3): scope-gate both legs (Q-K),
// dispatch them in parallel (Q-C), then merge (Q-A) / partial-fail (Q-E) /
// both-fail-terminal (BR3-3), billing the successful legs (Q-F). legs is the 2
// resolved concrete leg ids (ab_selected_models); scopeModels is the key's
// scope.models ("" / nil → all allowed).
func (h *ChatCompletionsHandler) dispatchAB(w http.ResponseWriter, r *http.Request, req *ChatRequest, apiKeyID string, legs, scopeModels []string) {
	ctx := r.Context()

	// Q-K / Architect High-2 — BOTH legs MUST pass the key's scope.models gate
	// BEFORE any dispatch; an out-of-scope leg → 403 for the WHOLE request (the
	// user named it explicitly; NOT a silent drop). The upstream keypolicy
	// middleware already gated body.model; this is the additive leg check.
	for _, leg := range legs {
		if !keypolicy.CheckModelScope(leg, scopeModels) {
			_ = openaierr.Write(w, ctx, http.StatusForbidden, "403_model_not_in_scope",
				"The selected model is not available for this key.", nil)
			return
		}
	}

	// PARALLEL dispatch (Q-C / BR2-3). Each leg is a single concrete-pin attempt
	// — legs do NOT failover (Q-D) and fail independently (Q-E).
	results := make([]abLegResult, len(legs))
	var wg sync.WaitGroup
	for i, leg := range legs {
		results[i].model = leg
		handle, ok := h.resolveLeg(leg)
		if !ok {
			// BR3-4 — an unresolved leg is a FAILED leg (502 marker), never a panic.
			results[i].err = errUpstreamInvalidResponse
			continue
		}
		wg.Add(1)
		go func(i int, handle adapterclient.ClientHandle) {
			defer wg.Done()
			results[i].resp, results[i].err = h.dispatchAdapterOnce(ctx, req, results[i].model, handle)
		}(i, handle)
	}
	wg.Wait()

	served := make([]string, 0, len(results))
	failed := make([]string, 0, len(results))
	total := 0
	for i := range results {
		if results[i].err == nil && results[i].resp != nil {
			served = append(served, results[i].model)
			total += results[i].resp.Usage.TotalTokens
		} else {
			failed = append(failed, results[i].model)
		}
	}

	// BR3-3 — ALL legs failed: ONE terminal §5.1.2 envelope (representative code
	// 504>502), zero billing. Reuses the 6.3 terminal writer.
	if len(served) == 0 {
		h.router.RecordABOutcome(ctx, routingclient.ABOutcomeBothFailed)
		h.logAB(ctx, legs, served, failed)
		h.writeFailoverTerminal(w, ctx, req, apiKeyID, representativeABError(results))
		return
	}

	outcome := routingclient.ABOutcomeBothOK
	if len(served) < len(legs) {
		outcome = routingclient.ABOutcomePartial // Q-E best-effort ≥1-leg
	}
	h.router.RecordABOutcome(ctx, outcome)

	merged := mergeABResponses(ctx, results, h.newID(), h.now().UTC().Unix())

	// BR2-7 — NO X-He-Selected-Model on the A/B path (it names ONE model). The
	// X-He-AB-Models response header lists the SUCCESSFULLY-served ids in order
	// (Q-E). (X-He-Cost-Usd stays unwired here — the gateway has no USD pricing
	// on the hot path; dual-billing is the token-based TPMDeduct below.)
	w.Header().Set(routingclient.ABModelsHeader, strings.Join(served, ","))
	writeChatJSON(w, http.StatusOK, merged)

	// Q-F dual-billing INVARIANT — ONE TPMDeduct of the SUMMED successful-leg
	// total_tokens (two real upstream calls → two real costs). A failed leg
	// carries no usage → not charged.
	h.tokenDeducter.TPMDeduct(ctx, apiKeyID, total)

	// Story 7.1 — A/B dual-billing: emit ONE usage.recorded event per SUCCESSFUL
	// leg (is_ab_leg=true, ledger_key={he_request_id}:{i}) so billing-svc writes
	// two ledger rows → summed deduction (parity with the 6.4 TPM dual-billing).
	// A failed leg carries no usage → no event (BR-D-7 partial parity).
	for i := range results {
		if results[i].err == nil && results[i].resp != nil {
			h.emitUsage(ctx, apiKeyID, results[i].model, results[i].resp.Usage, false, true, i)
		}
	}
	h.logAB(ctx, legs, served, failed)
}

// resolveLeg resolves an A/B leg's adapter handle. Returns ok=false (treated as
// a failed leg) when the registry is unset or the model is unregistered (BR3-4).
func (h *ChatCompletionsHandler) resolveLeg(model string) (adapterclient.ClientHandle, bool) {
	if h.adapterRegistry == nil {
		return nil, false
	}
	return h.adapterRegistry.Resolve(model)
}

// mergeABResponses builds the merged OpenAI chat.completion from the leg results
// (Q-A Option A / BR2-4): choices = concatenation with re-assigned index, each
// choice attributed via x_he_model; a failed leg contributes a marker choice
// (finish_reason="he_upstream_error" + x_he_error, Q-E/BR3-2). usage = SUMMED
// successful-leg tokens; single id/created/object; top-level model = leg A.
func mergeABResponses(ctx context.Context, legs []abLegResult, id string, created int64) *ChatResponse {
	out := &ChatResponse{ID: id, Object: "chat.completion", Created: created}
	if len(legs) > 0 {
		out.Model = legs[0].model // Q-A — top-level model is leg A's id
	}
	idx := 0
	var prompt, completion, total int
	for i := range legs {
		leg := legs[i]
		if leg.err == nil && leg.resp != nil {
			for _, ch := range leg.resp.Choices {
				ch.Index = idx
				ch.XHeModel = leg.model
				out.Choices = append(out.Choices, ch)
				idx++
			}
			prompt += leg.resp.Usage.PromptTokens
			completion += leg.resp.Usage.CompletionTokens
			total += leg.resp.Usage.TotalTokens
			continue
		}
		// Q-E / BR3-2 — failed-leg marker choice; the body stays a parseable
		// chat.completion and the client sees this leg's fate.
		out.Choices = append(out.Choices, ChatChoice{
			Index:        idx,
			Message:      ChatMessage{Role: "assistant"},
			FinishReason: "he_upstream_error",
			XHeModel:     leg.model,
			XHeError:     legErrorEnvelope(ctx, leg.err),
		})
		idx++
	}
	out.Usage = ChatUsage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: total}
	return out
}

// legErrorEnvelope builds the §5.1.2-shaped x_he_error for a failed leg (Q-E),
// reusing the 6.3 failover classification for the code/message + the canonical
// CodeMetadata error.type + the per-request he_request_id.
func legErrorEnvelope(ctx context.Context, err error) *XHeError {
	_, code, message, _ := classifyFailoverError(err)
	typ := "server_error"
	if meta, ok := openaierr.CodeMetadata[code]; ok {
		typ = meta.ErrorType
	}
	reqID, has := requestid.FromContext(ctx)
	if !has || reqID == "" {
		reqID = openaierr.SentinelHeRequestID
	}
	return &XHeError{Code: code, Message: message, Type: typ, Param: nil, HeRequestID: reqID}
}

// representativeABError picks the §5.1.2 code for the both-fail terminal (BR3-3):
// PREFER a leg that timed out (→ 504), else the last leg error (→ 502). Falls
// back to the invalid-response sentinel when no leg carried an error.
func representativeABError(results []abLegResult) error {
	var fallback error
	for i := range results {
		if e := results[i].err; e != nil {
			if _, code, _, _ := classifyFailoverError(e); code == "504_upstream_timeout" {
				return e // 504 wins (504 > 502 precedence)
			}
			fallback = e
		}
	}
	if fallback == nil {
		return errUpstreamInvalidResponse
	}
	return fallback
}

// logAB emits the Story-6.4 non-PII A/B slog line (AC4/BR4-1) — NEVER user_id /
// message content. ab_models = the requested legs; served/failed = the outcome.
func (h *ChatCompletionsHandler) logAB(ctx context.Context, abModels, served, failed []string) {
	heRequestID, _ := requestid.FromContext(ctx)
	h.logger.LogAttrs(ctx, slog.LevelInfo, "chat_completions_ab",
		slog.String("event", "chat_completions_ab"),
		slog.Any("ab_models", abModels),
		slog.Any("served_models", served),
		slog.Any("failed_models", failed),
		slog.String("he_request_id", heRequestID),
	)
}
