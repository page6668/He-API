// Command descgen regenerates the vendored adapter.pb.go rawDesc for Story 9.7
// (the additive Synthesize RPC + SynthesizeRequest/SynthesizeResponse messages).
// `buf` cannot run locally (project_toolchain_env_limits), so this throwaway
// generator reads the CURRENTLY-COMPILED FileDescriptor, appends the two new
// messages + the Synthesize method to its FileDescriptorProto programmatically,
// re-marshals, and prints the bytes as a Go double-quoted string literal ready
// to paste into `file_he_adapter_v1_adapter_proto_rawDesc`.
//
// Run: go run ./packages/proto/cmd/descgen  (from the repo root, before the
// matching hand-edits to the Go structs/getters/goTypes/depIdxs land — it reads
// the OLD descriptor and emits the NEW one). It is committed for reproducibility
// (the 9.6 descgen was throwaway; this one is kept as the documented procedure).
package main

import (
	"fmt"
	"strconv"

	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
)

func strp(s string) *string { return &s }
func i32p(i int32) *int32   { return &i }

func main() {
	fdp := protodesc.ToFileDescriptorProto(adapterv1.File_he_adapter_v1_adapter_proto)

	// Guard: only append if not already present (idempotent re-runs).
	for _, m := range fdp.MessageType {
		if m.GetName() == "SynthesizeRequest" {
			fmt.Println("// already present — nothing to do")
			return
		}
	}

	lbl := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
	tStr := descriptorpb.FieldDescriptorProto_TYPE_STRING
	tBytes := descriptorpb.FieldDescriptorProto_TYPE_BYTES
	tDouble := descriptorpb.FieldDescriptorProto_TYPE_DOUBLE

	synthReq := &descriptorpb.DescriptorProto{
		Name: strp("SynthesizeRequest"),
		Field: []*descriptorpb.FieldDescriptorProto{
			{Name: strp("model"), Number: i32p(1), Label: &lbl, Type: &tStr, JsonName: strp("model")},
			{Name: strp("input"), Number: i32p(2), Label: &lbl, Type: &tStr, JsonName: strp("input")},
			{Name: strp("voice"), Number: i32p(3), Label: &lbl, Type: &tStr, JsonName: strp("voice")},
			{Name: strp("response_format"), Number: i32p(4), Label: &lbl, Type: &tStr, JsonName: strp("responseFormat")},
			{Name: strp("speed"), Number: i32p(5), Label: &lbl, Type: &tDouble, JsonName: strp("speed"), OneofIndex: i32p(0), Proto3Optional: proto.Bool(true)},
			{Name: strp("he_request_id"), Number: i32p(6), Label: &lbl, Type: &tStr, JsonName: strp("heRequestId")},
		},
		OneofDecl: []*descriptorpb.OneofDescriptorProto{{Name: strp("_speed")}},
	}
	synthResp := &descriptorpb.DescriptorProto{
		Name: strp("SynthesizeResponse"),
		Field: []*descriptorpb.FieldDescriptorProto{
			{Name: strp("audio"), Number: i32p(1), Label: &lbl, Type: &tBytes, JsonName: strp("audio")},
			{Name: strp("mime_type"), Number: i32p(2), Label: &lbl, Type: &tStr, JsonName: strp("mimeType")},
		},
	}
	fdp.MessageType = append(fdp.MessageType, synthReq, synthResp)

	fdp.Service[0].Method = append(fdp.Service[0].Method, &descriptorpb.MethodDescriptorProto{
		Name:       strp("Synthesize"),
		InputType:  strp(".he.adapter.v1.SynthesizeRequest"),
		OutputType: strp(".he.adapter.v1.SynthesizeResponse"),
	})

	b, err := proto.Marshal(fdp)
	if err != nil {
		panic(err)
	}
	// strconv.Quote reproduces the EXACT bytes as a valid Go string literal
	// (non-UTF-8 bytes → \x escapes); the rawDesc only needs byte-fidelity, not
	// protoc-gen-go's per-\n line splitting.
	fmt.Println(strconv.Quote(string(b)))
}
