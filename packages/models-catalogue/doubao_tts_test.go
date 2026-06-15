// Story 9.7 (T4) — 9.7-UNIT-025: the doubao-tts catalogue entry. The new TTS
// model must register in BOTH Models + Capabilities (the 1:1 invariant stays
// GREEN — NewFromRegistry does not panic) with Chat:false / Transcription:false
// / Speech:true, and every existing model must remain Speech:false.
package modelscatalogue

import "testing"

func TestDoubaoTTS_CatalogueEntry(t *testing.T) {
	e, ok := DefaultCatalogue.Find("doubao-tts")
	if !ok {
		t.Fatal("doubao-tts missing from the default catalogue")
	}
	if e.Vendor != "bytedance" {
		t.Fatalf("doubao-tts vendor = %q, want bytedance", e.Vendor)
	}
	if e.Capabilities.Chat {
		t.Fatal("doubao-tts must be Chat:false (the BR-1.6 fence depends on it)")
	}
	if e.Capabilities.Transcription {
		t.Fatal("doubao-tts must be Transcription:false (it is not an ASR model)")
	}
	if !e.Capabilities.Speech {
		t.Fatal("doubao-tts must be Speech:true (gates ON /v1/audio/speech)")
	}
	// doubao-tts is the ONLY Speech:true model; every other model is Speech:false.
	for _, m := range DefaultCatalogue.List() {
		if m.ID == "doubao-tts" {
			continue
		}
		if m.Capabilities.Speech {
			t.Errorf("model %q unexpectedly Speech:true (only doubao-tts is speech-capable)", m.ID)
		}
	}
}
