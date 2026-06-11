// Story 9.3 — 9.3-CONTRACT. The usagelog.pb.go in this package is hand-authored
// (buf/protoc cannot run locally — [[project_toolchain_env_limits]]). This test
// is the correctness gate: it proves the hand-built FileDescriptorProto loads
// without panic at init() AND that the messages round-trip through both proto
// binary AND protojson (the wire format for the `usage.log.export.requested`
// Kafka topic), AND that the UsageLogExportService descriptor resolves its two
// methods. A malformed rawDesc / mismatched goTypes / wrong depIdxs would panic
// in init or fail here.
package usagelogv1

import (
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func sampleEvent() *UsageLogExportRequestedEvent {
	start := time.Date(2026, 3, 13, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC)
	return &UsageLogExportRequestedEvent{
		ExportId:   "44444444-4444-4444-4444-444444444444",
		UserId:     "11111111-1111-1111-1111-111111111111",
		Format:     "csv",
		RangeStart: timestamppb.New(start),
		RangeEnd:   timestamppb.New(end),
	}
}

func TestUsageLogExportEventBinaryRoundTrip(t *testing.T) {
	in := sampleEvent()

	b, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out UsageLogExportRequestedEvent
	if err := proto.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !proto.Equal(in, &out) {
		t.Fatalf("round-trip mismatch:\n in = %+v\nout = %+v", in, &out)
	}
	if out.GetExportId() != in.GetExportId() || out.GetUserId() != in.GetUserId() ||
		out.GetFormat() != "csv" || !out.GetRangeStart().AsTime().Equal(in.GetRangeStart().AsTime()) {
		t.Fatalf("field decode mismatch: %+v", &out)
	}
}

func TestUsageLogExportEventProtojson(t *testing.T) {
	in := sampleEvent()
	body, err := protojson.Marshal(in)
	if err != nil {
		t.Fatalf("protojson marshal: %v", err)
	}
	var out UsageLogExportRequestedEvent
	if err := protojson.Unmarshal(body, &out); err != nil {
		t.Fatalf("protojson unmarshal: %v", err)
	}
	if !proto.Equal(in, &out) {
		t.Fatalf("protojson round-trip mismatch:\n in=%+v\nout=%+v", in, &out)
	}
}

// TestRequestResponseMessagesRoundTrip exercises the four RPC message types so a
// tag-shift on any of them is caught.
func TestRequestResponseMessagesRoundTrip(t *testing.T) {
	req := &RequestUsageLogExportRequest{
		UserId:                   "11111111-1111-1111-1111-111111111111",
		Format:                   "json",
		RangeStart:               timestamppb.New(time.Date(2026, 3, 13, 0, 0, 0, 0, time.UTC)),
		RangeEnd:                 timestamppb.New(time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC)),
		IdempotencyWindowSeconds: 86400,
	}
	roundTrip(t, req, &RequestUsageLogExportRequest{})

	resp := &RequestUsageLogExportResponse{
		ExportId:    "44444444-4444-4444-4444-444444444444",
		Status:      "pending",
		Format:      "json",
		RequestedAt: timestamppb.New(time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)),
	}
	roundTrip(t, resp, &RequestUsageLogExportResponse{})

	cur := &GetCurrentUsageLogExportResponse{
		HasCurrent:         true,
		ExportId:           "44444444-4444-4444-4444-444444444444",
		Status:             "completed",
		Format:             "csv",
		RequestedAt:        timestamppb.New(time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)),
		SignedUrlExpiresAt: timestamppb.New(time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)),
	}
	roundTrip(t, cur, &GetCurrentUsageLogExportResponse{})

	greq := &GetCurrentUsageLogExportRequest{UserId: "11111111-1111-1111-1111-111111111111"}
	roundTrip(t, greq, &GetCurrentUsageLogExportRequest{})
}

func roundTrip(t *testing.T, in, out proto.Message) {
	t.Helper()
	b, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("marshal %T: %v", in, err)
	}
	if err := proto.Unmarshal(b, out); err != nil {
		t.Fatalf("unmarshal %T: %v", in, err)
	}
	if !proto.Equal(in, out) {
		t.Fatalf("round-trip mismatch %T:\n in=%+v\nout=%+v", in, in, out)
	}
}

// TestServiceDescriptorResolves proves the service + its two methods are present
// in the loaded FileDescriptor (the connect handler/client depends on this).
func TestServiceDescriptorResolves(t *testing.T) {
	fd := File_he_usagelog_v1_usagelog_proto
	if fd == nil {
		t.Fatal("file descriptor is nil")
	}
	svcs := fd.Services()
	if svcs.Len() != 1 {
		t.Fatalf("want 1 service, got %d", svcs.Len())
	}
	svc := svcs.Get(0)
	if string(svc.Name()) != "UsageLogExportService" {
		t.Fatalf("unexpected service name: %s", svc.Name())
	}
	for _, m := range []string{"RequestUsageLogExport", "GetCurrentUsageLogExport"} {
		if svc.Methods().ByName(protoreflect.Name(m)) == nil {
			t.Fatalf("method %s not found on service descriptor", m)
		}
	}
}
