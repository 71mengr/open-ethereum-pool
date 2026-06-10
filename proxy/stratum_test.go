package proxy

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestTCPHandleEthGetWorkReturnsCurrentTemplate(t *testing.T) {
	server := &ProxyServer{config: &Config{}}
	server.diff = "0x00000000ffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	server.blockTemplate.Store(&BlockTemplate{
		Header: "0x1111111111111111111111111111111111111111111111111111111111111111",
		Seed:   "0x2222222222222222222222222222222222222222222222222222222222222222",
	})

	var buf bytes.Buffer
	session := &Session{ip: "127.0.0.1", enc: json.NewEncoder(&buf)}
	req := &StratumReq{Id: json.RawMessage(`1`), Method: "eth_getWork", Params: json.RawMessage(`[]`)}

	if err := session.handleTCPMessage(server, req); err != nil {
		t.Fatalf("eth_getWork returned error: %v", err)
	}

	var resp JSONRpcResp
	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("expected no error, got %v", resp.Error)
	}

	result, ok := resp.Result.([]interface{})
	if !ok {
		t.Fatalf("expected result array, got %T", resp.Result)
	}
	want := []string{
		"0x1111111111111111111111111111111111111111111111111111111111111111",
		"0x2222222222222222222222222222222222222222222222222222222222222222",
		"0x00000000ffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
	}
	if len(result) != len(want) {
		t.Fatalf("expected %d work fields, got %d", len(want), len(result))
	}
	for i := range want {
		if result[i] != want[i] {
			t.Fatalf("expected result[%d] %s, got %v", i, want[i], result[i])
		}
	}
}

func TestTCPHandleEthGetWorkReturnsWorkNotReady(t *testing.T) {
	server := &ProxyServer{config: &Config{}}

	var buf bytes.Buffer
	session := &Session{ip: "127.0.0.1", enc: json.NewEncoder(&buf)}
	req := &StratumReq{Id: json.RawMessage(`2`), Method: "eth_getWork", Params: json.RawMessage(`[]`)}

	if err := session.handleTCPMessage(server, req); err == nil {
		t.Fatalf("expected work-not-ready error")
	}

	var resp JSONRpcResp
	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	if resp.Error == nil {
		t.Fatalf("expected error response")
	}
}
