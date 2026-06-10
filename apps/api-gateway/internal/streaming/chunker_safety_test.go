// Story 8.3 (AC2) — chunker-level §9.3 出参 guard seam tests for BOTH chunkers
// (MockChunker + AdapterChunker). Covers: terminate-on-hit with an EMPTY
// content_filter terminal via the normal WriteEvent+WriteDone path (NOT an error
// frame, M-1), single terminal + [DONE], nil-guard byte-identity (zero overhead
// when unwired / clean), and the first-delta hit (headers flush on the terminal).
//
// The guard is the REAL contentsafety.StreamGuard over the DefaultLexicon (flags
// the known en/abuse/high term "badword"), so these are true integration tests of
// the seam — not a fake guard.
//
// Scenario IDs trace to docs/qa/assessments/8.3-test-design-20260611.md.
package streaming

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/he-api/he-api/apps/api-gateway/internal/contentsafety"
	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
	safetylexicon "github.com/he-api/he-api/packages/safety-lexicon"
)

func realGuard() *contentsafety.StreamGuard {
	return contentsafety.NewStreamGuard(contentsafety.NewScanner(safetylexicon.DefaultLexicon))
}

// assertFilteredTerminal verifies a filtered stream body: exactly one
// content_filter terminal, ends with [DONE], carries NO error envelope and NO
// matched substring.
func assertFilteredTerminal(t *testing.T, body string) {
	t.Helper()
	if n := strings.Count(body, "content_filter"); n != 1 {
		t.Fatalf("want exactly 1 content_filter terminal, got %d in:\n%s", n, body)
	}
	if !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("filtered body must end with data: [DONE]; got:\n%s", body)
	}
	if strings.Contains(body, `"error"`) {
		t.Fatalf("filtered stream emitted an ERROR envelope (must use a normal terminal, M-1):\n%s", body)
	}
	if strings.Contains(body, "badword") {
		t.Fatalf("filtered stream leaked the matched term:\n%s", body)
	}
}

// 8.3-INT-015 (mock) — MockChunker guard seam terminates on a hit with a single
// content_filter terminal + [DONE], no error frame.
func TestMockChunker_OutputGuard_TerminatesOnHit(t *testing.T) {
	chunker := NewMockChunker("qwen-max", "here is a clean prefix then badword and a tail", "id-1", 1700000000)
	chunker.AttachOutputGuard(realGuard())

	rec := httptest.NewRecorder()
	chunks, _, err := chunker.Stream(context.Background(), NewWriter(rec))
	if err != ErrContentFiltered {
		t.Fatalf("err = %v, want ErrContentFiltered", err)
	}
	if chunks < 1 {
		t.Fatalf("chunksEmitted = %d, want >=1 (bootstrap + terminal)", chunks)
	}
	assertFilteredTerminal(t, rec.Body.String())
}

// 8.3-INT-015 (adapter) — AdapterChunker guard seam terminates on a hit.
func TestAdapterChunker_OutputGuard_TerminatesOnHit(t *testing.T) {
	stream := &fakeAdapterStream{chunks: []*adapterv1.ChatChunk{
		chunkDelta("chatcmpl-1", "assistant", "", nil),
		chunkDelta("chatcmpl-1", "", "clean start ", nil),
		chunkDelta("chatcmpl-1", "", "badword", nil), // completes the term → withheld
		chunkDelta("chatcmpl-1", "", " never reached", nil),
	}}
	chunker := NewAdapterChunker(stream, "deepseek-v3")
	chunker.AttachOutputGuard(realGuard())

	rec := httptest.NewRecorder()
	_, _, err := chunker.Stream(context.Background(), NewWriter(rec))
	if err != ErrContentFiltered {
		t.Fatalf("err = %v, want ErrContentFiltered", err)
	}
	body := rec.Body.String()
	assertFilteredTerminal(t, body)
	if strings.Contains(body, "never reached") {
		t.Fatalf("forwarded an upstream delta AFTER the §9.3 cut:\n%s", body)
	}
	// The clean prefix delta WAS forwarded (emit-as-produced); only the
	// term-completing delta is withheld (scan-before-forward, OQ-8.3-2).
	if !strings.Contains(body, "clean start") {
		t.Fatalf("clean prefix delta was not forwarded before the cut:\n%s", body)
	}
}

// 8.3-INT-011 (chunker half) — a term SPLIT across deltas terminates at the seam;
// the partial first delta IS on the wire (logged, unrecoverable), the completing
// delta is withheld.
func TestAdapterChunker_OutputGuard_SplitAcrossDeltas(t *testing.T) {
	stream := &fakeAdapterStream{chunks: []*adapterv1.ChatChunk{
		chunkDelta("c", "", "bad", nil),  // partial — forwarded (does not match alone)
		chunkDelta("c", "", "word", nil), // completes "badword" — withheld + terminate
	}}
	chunker := NewAdapterChunker(stream, "deepseek-v3")
	chunker.AttachOutputGuard(realGuard())

	rec := httptest.NewRecorder()
	_, _, err := chunker.Stream(context.Background(), NewWriter(rec))
	if err != ErrContentFiltered {
		t.Fatalf("err = %v, want ErrContentFiltered", err)
	}
	assertFilteredTerminal(t, rec.Body.String())
}

// 8.3-INT-012 — hit on the very FIRST delta (headers not yet flushed) → the
// terminal write flushes headers → a 200 SSE content_filter response, NOT a JSON
// error envelope.
func TestAdapterChunker_OutputGuard_HitOnFirstDelta(t *testing.T) {
	stream := &fakeAdapterStream{chunks: []*adapterv1.ChatChunk{
		chunkDelta("c", "", "badword", nil), // first delta completes the term
	}}
	chunker := NewAdapterChunker(stream, "deepseek-v3")
	chunker.AttachOutputGuard(realGuard())

	rec := httptest.NewRecorder()
	w := NewWriter(rec)
	_, _, err := chunker.Stream(context.Background(), w)
	if err != ErrContentFiltered {
		t.Fatalf("err = %v, want ErrContentFiltered", err)
	}
	if !w.HeadersFlushed() {
		t.Fatal("headers not flushed — the terminal write must flush them")
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, "data: ") {
		t.Fatalf("first-delta hit must yield an SSE body, not a JSON envelope:\n%s", body)
	}
	assertFilteredTerminal(t, body)
}

// 8.3-INT-016 / 8.3-UNIT-034 — clean stream byte-identity: a clean stream WITH the
// guard attached is byte-identical to the same stream with NO guard (the guard
// adds nothing observable when clean / unwired).
func TestMockChunker_GuardClean_ByteIdentical(t *testing.T) {
	const content = "a perfectly clean assistant answer with several words here"
	noGuard := httptest.NewRecorder()
	if _, _, err := NewMockChunker("m", content, "id", 1700000000).Stream(context.Background(), NewWriter(noGuard)); err != nil {
		t.Fatalf("no-guard stream err = %v", err)
	}
	withGuard := httptest.NewRecorder()
	gc := NewMockChunker("m", content, "id", 1700000000)
	gc.AttachOutputGuard(realGuard())
	if _, _, err := gc.Stream(context.Background(), NewWriter(withGuard)); err != nil {
		t.Fatalf("with-guard clean stream err = %v", err)
	}
	if noGuard.Body.String() != withGuard.Body.String() {
		t.Fatalf("clean stream diverged with the guard attached:\n no-guard=%q\n guard   =%q", noGuard.Body.String(), withGuard.Body.String())
	}
}

func TestAdapterChunker_GuardClean_ByteIdentical(t *testing.T) {
	mk := func() *fakeAdapterStream {
		stop := "stop"
		return &fakeAdapterStream{chunks: []*adapterv1.ChatChunk{
			chunkDelta("c", "assistant", "", nil),
			chunkDelta("c", "", "Hello", nil),
			chunkDelta("c", "", " world", &stop),
			chunkTerminal("c", 4, 2, 6),
		}}
	}
	noGuard := httptest.NewRecorder()
	if _, _, err := NewAdapterChunker(mk(), "deepseek-v3").Stream(context.Background(), NewWriter(noGuard)); err != nil {
		t.Fatalf("no-guard err = %v", err)
	}
	withGuard := httptest.NewRecorder()
	gc := NewAdapterChunker(mk(), "deepseek-v3")
	gc.AttachOutputGuard(realGuard())
	if _, _, err := gc.Stream(context.Background(), NewWriter(withGuard)); err != nil {
		t.Fatalf("with-guard clean err = %v", err)
	}
	if noGuard.Body.String() != withGuard.Body.String() {
		t.Fatalf("clean adapter stream diverged with the guard attached:\n no-guard=%q\n guard   =%q", noGuard.Body.String(), withGuard.Body.String())
	}
}
