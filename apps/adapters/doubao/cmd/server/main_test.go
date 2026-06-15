package main

import "testing"

// TestASRBootGate covers the M-2 (Round-2 Architect ruling) conditional boot
// fail-fast matrix on the pure gate helper. main() calls os.Exit on a tripped
// gate, so the decision logic is extracted into asrBootGateError to keep it
// deterministically testable (no process spawn).
//
// Matrix (T3.3 (a)/(b)/(c)):
//
//	(a) doubao-asr bound   + ASR env unset → fatal (gate trips)
//	(b) doubao-asr unbound + ASR env unset → boots (request-time CodeUnavailable)
//	(c) doubao-asr bound   + ASR env set   → boots (serves ASR)
func TestASRBootGate(t *testing.T) {
	tests := []struct {
		name          string
		boundModelIDs []string
		asrConfigured bool
		wantErr       bool
	}{
		{
			name:          "(a) asr bound + unconfigured → boot fatal",
			boundModelIDs: []string{"doubao-pro", "doubao-lite", asrModelID},
			asrConfigured: false,
			wantErr:       true,
		},
		{
			name:          "(b) asr NOT bound + unconfigured → boots (no gate)",
			boundModelIDs: []string{"doubao-pro", "doubao-lite"},
			asrConfigured: false,
			wantErr:       false,
		},
		{
			name:          "(c) asr bound + configured → boots (serves ASR)",
			boundModelIDs: []string{"doubao-pro", "doubao-lite", asrModelID},
			asrConfigured: true,
			wantErr:       false,
		},
		{
			name:          "chat-only default + unconfigured → boots (no shared-service regression)",
			boundModelIDs: defaultBoundModelIDs,
			asrConfigured: false,
			wantErr:       false,
		},
		{
			name:          "asr-only bound + configured → boots",
			boundModelIDs: []string{asrModelID},
			asrConfigured: true,
			wantErr:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := asrBootGateError(tt.boundModelIDs, tt.asrConfigured)
			if tt.wantErr && err == nil {
				t.Fatalf("asrBootGateError(%v, %v) = nil; want error", tt.boundModelIDs, tt.asrConfigured)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("asrBootGateError(%v, %v) = %v; want nil", tt.boundModelIDs, tt.asrConfigured, err)
			}
		})
	}
}
