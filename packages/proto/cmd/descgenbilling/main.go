// Command descgenbilling regenerates the vendored billing.pb.go rawDesc for
// Story 9.7 (additive BILLING_MODE_PER_CHARACTER=4 enum value +
// UsageEvent.character_count=15 field). `buf` cannot run locally
// (project_toolchain_env_limits); this reads the currently-compiled
// FileDescriptor, appends the enum value + field programmatically, re-marshals,
// and prints the bytes as a Go double-quoted string literal ready to paste into
// file_he_billing_v1_billing_proto_rawDesc.
package main

import (
	"fmt"
	"strconv"

	billingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/billing/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
)

func strp(s string) *string { return &s }
func i32p(i int32) *int32   { return &i }

func main() {
	fdp := protodesc.ToFileDescriptorProto(billingv1.File_he_billing_v1_billing_proto)

	// Append BILLING_MODE_PER_CHARACTER = 4 to the BillingMode enum.
	for _, e := range fdp.EnumType {
		if e.GetName() != "BillingMode" {
			continue
		}
		for _, v := range e.Value {
			if v.GetName() == "BILLING_MODE_PER_CHARACTER" {
				fmt.Println("// enum value already present")
				return
			}
		}
		e.Value = append(e.Value, &descriptorpb.EnumValueDescriptorProto{
			Name:   strp("BILLING_MODE_PER_CHARACTER"),
			Number: i32p(4),
		})
	}

	// Append character_count = 15 (uint32) to UsageEvent.
	lbl := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
	tU32 := descriptorpb.FieldDescriptorProto_TYPE_UINT32
	for _, m := range fdp.MessageType {
		if m.GetName() != "UsageEvent" {
			continue
		}
		m.Field = append(m.Field, &descriptorpb.FieldDescriptorProto{
			Name:     strp("character_count"),
			Number:   i32p(15),
			Label:    &lbl,
			Type:     &tU32,
			JsonName: strp("characterCount"),
		})
	}

	b, err := proto.Marshal(fdp)
	if err != nil {
		panic(err)
	}
	fmt.Println(strconv.Quote(string(b)))
}
