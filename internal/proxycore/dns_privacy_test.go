package proxycore

import (
	"context"
	"testing"

	"github.com/metacubex/mihomo/component/resolver"
	D "github.com/miekg/dns"
)

type countingDNSResolver struct {
	resolver.Resolver
	requests int
}

func (r *countingDNSResolver) ExchangeContext(_ context.Context, query *D.Msg) (*D.Msg, error) {
	r.requests++
	return new(D.Msg).SetReply(query), nil
}

func TestMultiQuestionPrivateDNSNeverReachesUpstream(t *testing.T) {
	core := &Core{}
	if err := core.setMesh(nil, nil); err != nil {
		t.Fatal(err)
	}
	upstream := &countingDNSResolver{}
	r := &meshResolver{Resolver: upstream, core: core}
	for _, names := range [][]string{
		{"mac.jungo.internal.", "example.com."},
		{"example.com.", "mac.jungo.internal."},
		{"mac.jungo.internal.", "server.jungo.internal."},
	} {
		query := new(D.Msg)
		for _, name := range names {
			query.Question = append(query.Question, D.Question{Name: name, Qtype: D.TypeA, Qclass: D.ClassINET})
		}
		if _, err := r.ExchangeContext(context.Background(), query); err == nil {
			t.Fatal("unsupported multi-question request accepted")
		}
		if upstream.requests != 0 {
			t.Fatal("private device DNS escaped to public upstream")
		}
	}
	public := new(D.Msg).SetQuestion("example.com.", D.TypeA)
	if _, err := r.ExchangeContext(context.Background(), public); err != nil || upstream.requests != 1 {
		t.Fatalf("public DNS forwarding regressed: count=%d, error=%v", upstream.requests, err)
	}
}
