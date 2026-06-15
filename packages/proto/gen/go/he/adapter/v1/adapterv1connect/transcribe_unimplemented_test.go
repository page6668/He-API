// Story 9.6 (T1) — INT-007 (representative): an adapter that serves Chat but
// embeds UnimplementedAdapterServiceHandler (the 5 non-Doubao adapters) returns
// connect.CodeUnimplemented for Transcribe over the wire — the additive-RPC
// blast-radius proof. The per-adapter build-green proof is T7/INT-011.
package adapterv1connect_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
	"github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1/adapterv1connect"
)

// chatOnlyAdapter models a non-Doubao adapter: it serves Chat and inherits the
// Unimplemented Transcribe stub.
type chatOnlyAdapter struct {
	adapterv1connect.UnimplementedAdapterServiceHandler
}

func (chatOnlyAdapter) Chat(_ context.Context, _ *connect.Request[v1.ChatRequest], stream *connect.ServerStream[v1.ChatChunk]) error {
	return stream.Send(&v1.ChatChunk{Id: "chatcmpl-test", Object: "chat.completion"})
}

func TestTranscribe_UnimplementedOnChatOnlyAdapter(t *testing.T) {
	mux := http.NewServeMux()
	path, handler := adapterv1connect.NewAdapterServiceHandler(chatOnlyAdapter{})
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := adapterv1connect.NewAdapterServiceClient(srv.Client(), srv.URL)

	// Transcribe → CodeUnimplemented.
	_, err := client.Transcribe(context.Background(), connect.NewRequest(&v1.TranscribeRequest{Model: "doubao-asr"}))
	if err == nil {
		t.Fatal("expected CodeUnimplemented, got nil error")
	}
	if got := connect.CodeOf(err); got != connect.CodeUnimplemented {
		t.Fatalf("Transcribe error code = %v, want CodeUnimplemented", got)
	}

	// Chat still works on the same adapter (additive RPC did not break it).
	stream, err := client.Chat(context.Background(), connect.NewRequest(&v1.ChatRequest{Model: "deepseek-v3"}))
	if err != nil {
		t.Fatalf("Chat call failed: %v", err)
	}
	if !stream.Receive() {
		t.Fatalf("Chat stream produced no chunk: %v", stream.Err())
	}
	if stream.Msg().GetId() != "chatcmpl-test" {
		t.Fatalf("unexpected chunk: %+v", stream.Msg())
	}
	_ = stream.Close()
}
