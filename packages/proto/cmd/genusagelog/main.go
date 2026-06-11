// Command genusagelog builds the FileDescriptorProto for
// he/usagelog/v1/usagelog.proto and prints its byte-exact wire encoding as a
// Go `\xNN` string literal, for pasting into the hand-authored usagelog.pb.go.
//
// buf/protoc cannot run in this environment ([[project_toolchain_env_limits]]);
// this mirrors the Story 9.1 analytics.pb.go hand-author pattern (the throwaway
// generator builds the descriptor via descriptorpb, which IS in the runtime).
// The paired usagelog_roundtrip_test.go (9.3-CONTRACT) is the durable gate:
// it proves the rawDesc loads without panic at init() and that every message
// round-trips through proto binary + protojson, and that the service descriptor
// resolves its 2 methods.
//
// Run:  go run ./cmd/genusagelog
package main

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

func strField(name string, num int32, json string) *descriptorpb.FieldDescriptorProto {
	return &descriptorpb.FieldDescriptorProto{
		Name:     proto.String(name),
		Number:   proto.Int32(num),
		Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
		Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
		JsonName: proto.String(json),
	}
}

func int32Field(name string, num int32, json string) *descriptorpb.FieldDescriptorProto {
	return &descriptorpb.FieldDescriptorProto{
		Name:     proto.String(name),
		Number:   proto.Int32(num),
		Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
		Type:     descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(),
		JsonName: proto.String(json),
	}
}

func boolField(name string, num int32, json string) *descriptorpb.FieldDescriptorProto {
	return &descriptorpb.FieldDescriptorProto{
		Name:     proto.String(name),
		Number:   proto.Int32(num),
		Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
		Type:     descriptorpb.FieldDescriptorProto_TYPE_BOOL.Enum(),
		JsonName: proto.String(json),
	}
}

func tsField(name string, num int32, json string) *descriptorpb.FieldDescriptorProto {
	return &descriptorpb.FieldDescriptorProto{
		Name:     proto.String(name),
		Number:   proto.Int32(num),
		Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
		Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
		TypeName: proto.String(".google.protobuf.Timestamp"),
		JsonName: proto.String(json),
	}
}

func main() {
	fd := &descriptorpb.FileDescriptorProto{
		Name:       proto.String("he/usagelog/v1/usagelog.proto"),
		Package:    proto.String("he.usagelog.v1"),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"google/protobuf/timestamp.proto"},
		Options: &descriptorpb.FileOptions{
			GoPackage: proto.String("github.com/he-api/he-api/packages/proto/gen/go/he/usagelog/v1;usagelogv1"),
		},
		MessageType: []*descriptorpb.DescriptorProto{
			// 0: UsageLogExportRequestedEvent (the usage.log.export.requested Kafka payload)
			{
				Name: proto.String("UsageLogExportRequestedEvent"),
				Field: []*descriptorpb.FieldDescriptorProto{
					strField("export_id", 1, "exportId"),
					strField("user_id", 2, "userId"),
					strField("format", 3, "format"),
					tsField("range_start", 4, "rangeStart"),
					tsField("range_end", 5, "rangeEnd"),
				},
			},
			// 1: RequestUsageLogExportRequest
			{
				Name: proto.String("RequestUsageLogExportRequest"),
				Field: []*descriptorpb.FieldDescriptorProto{
					strField("user_id", 1, "userId"),
					strField("format", 2, "format"),
					tsField("range_start", 3, "rangeStart"),
					tsField("range_end", 4, "rangeEnd"),
					int32Field("idempotency_window_seconds", 5, "idempotencyWindowSeconds"),
				},
			},
			// 2: RequestUsageLogExportResponse
			{
				Name: proto.String("RequestUsageLogExportResponse"),
				Field: []*descriptorpb.FieldDescriptorProto{
					strField("export_id", 1, "exportId"),
					strField("status", 2, "status"),
					strField("format", 3, "format"),
					tsField("requested_at", 4, "requestedAt"),
				},
			},
			// 3: GetCurrentUsageLogExportRequest
			{
				Name: proto.String("GetCurrentUsageLogExportRequest"),
				Field: []*descriptorpb.FieldDescriptorProto{
					strField("user_id", 1, "userId"),
				},
			},
			// 4: GetCurrentUsageLogExportResponse
			{
				Name: proto.String("GetCurrentUsageLogExportResponse"),
				Field: []*descriptorpb.FieldDescriptorProto{
					boolField("has_current", 1, "hasCurrent"),
					strField("export_id", 2, "exportId"),
					strField("status", 3, "status"),
					strField("format", 4, "format"),
					tsField("requested_at", 5, "requestedAt"),
					tsField("signed_url_expires_at", 6, "signedUrlExpiresAt"),
				},
			},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{
				Name: proto.String("UsageLogExportService"),
				Method: []*descriptorpb.MethodDescriptorProto{
					{
						Name:       proto.String("RequestUsageLogExport"),
						InputType:  proto.String(".he.usagelog.v1.RequestUsageLogExportRequest"),
						OutputType: proto.String(".he.usagelog.v1.RequestUsageLogExportResponse"),
					},
					{
						Name:       proto.String("GetCurrentUsageLogExport"),
						InputType:  proto.String(".he.usagelog.v1.GetCurrentUsageLogExportRequest"),
						OutputType: proto.String(".he.usagelog.v1.GetCurrentUsageLogExportResponse"),
					},
				},
			},
		},
	}

	raw, err := proto.Marshal(fd)
	if err != nil {
		panic(err)
	}

	var b strings.Builder
	b.WriteString("const file_he_usagelog_v1_usagelog_proto_rawDesc = \"\" +\n\t\"")
	for i, c := range raw {
		fmt.Fprintf(&b, "\\x%02x", c)
		// wrap every 24 bytes for readability
		if (i+1)%24 == 0 && i != len(raw)-1 {
			b.WriteString("\" +\n\t\"")
		}
	}
	b.WriteString("\"\n")
	fmt.Println(b.String())
	fmt.Printf("// rawDesc len = %d bytes\n", len(raw))
}
