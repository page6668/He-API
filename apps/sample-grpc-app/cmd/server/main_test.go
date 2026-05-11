package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	samplev1 "github.com/he-api/he-api/packages/proto/gen/go/he/sample/v1"
	"github.com/he-api/he-api/packages/proto/gen/go/he/sample/v1/samplev1connect"
)

func TestPing(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle(samplev1connect.NewSampleServiceHandler(&SampleServer{}))

	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Build the connect client directly via connect.NewClient so the wire
	// procedure path is explicit (the generated factory wraps this same call).
	client := connect.NewClient[samplev1.PingRequest, samplev1.PingResponse](
		http.DefaultClient,
		srv.URL+samplev1connect.SampleServicePingProcedure,
	)
	resp, err := client.CallUnary(context.Background(), connect.NewRequest(&samplev1.PingRequest{Ping: "hello"}))
	if err != nil {
		t.Fatalf("Ping returned error: %v", err)
	}
	if resp.Msg == nil {
		t.Fatalf("Ping returned nil response message")
	}
	if got, want := resp.Msg.GetPong(), "hello"; got != want {
		t.Errorf("Ping pong = %q, want %q", got, want)
	}
}
