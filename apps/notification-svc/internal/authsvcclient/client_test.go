// Story 5.4 — 5.4-UNIT-053..057 (authsvcclient: field mapping, error
// propagation, request shape).
package authsvcclient

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"
)

type fakeUpstream struct {
	resp   *authv1.GetCapNotificationContextResponse
	err    error
	gotReq *authv1.GetCapNotificationContextRequest
}

func (f *fakeUpstream) GetCapNotificationContext(
	_ context.Context,
	req *connect.Request[authv1.GetCapNotificationContextRequest],
) (*connect.Response[authv1.GetCapNotificationContextResponse], error) {
	f.gotReq = req.Msg
	if f.err != nil {
		return nil, f.err
	}
	return connect.NewResponse(f.resp), nil
}

func TestClient_MapsAllFields(t *testing.T) {
	up := &fakeUpstream{resp: &authv1.GetCapNotificationContextResponse{
		UserEmail:            "alex@example.com",
		UserLocale:           "ja",
		UserDisplayName:      "Alex",
		KeyName:              "prod-key",
		KeyMonthlyCostCapUsd: "50.00",
	}}
	c := NewWithClient(up)

	cc, err := c.GetCapNotificationContext(context.Background(), "key-1")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if up.gotReq.GetApiKeyId() != "key-1" {
		t.Fatalf("api_key_id=%q", up.gotReq.GetApiKeyId())
	}
	if cc.UserEmail != "alex@example.com" || cc.UserLocale != "ja" ||
		cc.UserDisplayName != "Alex" || cc.KeyName != "prod-key" || cc.KeyMonthlyCostCapUSD != "50.00" {
		t.Fatalf("bad mapping: %+v", cc)
	}
}

func TestClient_PropagatesConnectError(t *testing.T) {
	up := &fakeUpstream{err: connect.NewError(connect.CodeNotFound, errors.New("api_key_not_found"))}
	c := NewWithClient(up)
	_, err := c.GetCapNotificationContext(context.Background(), "key-1")
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("code=%v want NotFound (preserved)", connect.CodeOf(err))
	}
}
