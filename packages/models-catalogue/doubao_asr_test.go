// Story 9.6 (T4) — 9.6-UNIT-020: the doubao-asr catalogue entry. The new ASR
// model must register in BOTH Models + Capabilities (the 1:1 invariant stays
// GREEN — NewFromRegistry does not panic) with Chat:false / Transcription:true,
// and every existing model must remain Transcription:false.
package modelscatalogue

import "testing"

func TestDoubaoASR_CatalogueEntry(t *testing.T) {
	// DefaultCatalogue is built at package init via NewFromRegistry — if the
	// 1:1 invariant were violated, the package would have panicked at init.
	e, ok := DefaultCatalogue.Find("doubao-asr")
	if !ok {
		t.Fatal("doubao-asr missing from the default catalogue")
	}
	if e.Vendor != "bytedance" {
		t.Fatalf("doubao-asr vendor = %q, want bytedance", e.Vendor)
	}
	if e.Capabilities.Chat {
		t.Fatal("doubao-asr must be Chat:false (the BR-1.6 fence depends on it)")
	}
	if !e.Capabilities.Transcription {
		t.Fatal("doubao-asr must be Transcription:true")
	}
	// doubao-asr is the ONLY Transcription:true model; every other model is
	// Transcription:false. (Post-9.7 the non-chat set is {doubao-asr, doubao-tts}
	// — that broader fence is asserted in doubao_tts_test.go.)
	for _, m := range DefaultCatalogue.List() {
		if m.ID == "doubao-asr" {
			continue
		}
		if m.Capabilities.Transcription {
			t.Errorf("model %q unexpectedly Transcription:true", m.ID)
		}
	}
}

// 9.6-UNIT-020 (updated for 9.7) — declaration order preserved: doubao-asr keeps
// its index 13 (penultimate) and doubao-tts is appended LAST at index 14, so the
// existing index ordering (0-12) is unperturbed (BR-1.10 load-bearing order).
func TestDoubaoASR_AppendedLast(t *testing.T) {
	list := DefaultCatalogue.List()
	if len(list) != 15 {
		t.Fatalf("catalogue size = %d, want 15 (9.7 appended doubao-tts)", len(list))
	}
	if got := list[13].ID; got != "doubao-asr" {
		t.Fatalf("index 13 = %q, want doubao-asr (9.6 position preserved)", got)
	}
	if got := list[14].ID; got != "doubao-tts" {
		t.Fatalf("last catalogue entry = %q, want doubao-tts (9.7 appended last)", got)
	}
}
