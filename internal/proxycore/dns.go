package proxycore

import (
	"context"
	"net"
	"net/netip"
	"strings"

	"github.com/metacubex/mihomo/component/resolver"
	D "github.com/miekg/dns"
)

// Private DNS is answered before any public upstream or imported hosts data.
// Unknown private names return NXDOMAIN, never a public DNS query.
type meshDNSService struct {
	core     *Core
	fallback resolver.Service
}

func (s *meshDNSService) ServeMsg(ctx context.Context, request *D.Msg) (*D.Msg, error) {
	if s.core.closed.Load() {
		return nil, net.ErrClosed
	}
	if len(request.Question) != 1 {
		return nil, resolver.ErrIPNotFound
	}
	question := request.Question[0]
	if !privateHost(question.Name) {
		return s.fallback.ServeMsg(ctx, request)
	}
	response := new(D.Msg)
	response.SetReply(request)
	response.Authoritative = true
	state := s.core.mesh.Load()
	ip, ok := state.names[strings.ToLower(strings.TrimSuffix(question.Name, "."))]
	if !ok || state.node == nil {
		response.Rcode = D.RcodeNameError
		return response, nil
	}
	if question.Qclass == D.ClassINET && question.Qtype == D.TypeA {
		response.Answer = []D.RR{&D.A{Hdr: D.RR_Header{Name: question.Name, Rrtype: D.TypeA, Class: D.ClassINET, Ttl: 5}, A: net.IP(ip.AsSlice())}}
	}
	return response, nil
}

type meshResolver struct {
	resolver.Resolver
	core *Core
}

func (r *meshResolver) lookup(host string) ([]netip.Addr, error) {
	state := r.core.mesh.Load()
	ip, ok := state.names[strings.ToLower(strings.TrimSuffix(host, "."))]
	if r.core.closed.Load() || state.node == nil || !ok {
		return nil, resolver.ErrIPNotFound
	}
	return []netip.Addr{ip}, nil
}
func (r *meshResolver) LookupIP(ctx context.Context, host string) ([]netip.Addr, error) {
	if privateHost(host) {
		return r.lookup(host)
	}
	return r.Resolver.LookupIP(ctx, host)
}
func (r *meshResolver) LookupIPv4(ctx context.Context, host string) ([]netip.Addr, error) {
	if privateHost(host) {
		return r.lookup(host)
	}
	return r.Resolver.LookupIPv4(ctx, host)
}
func (r *meshResolver) LookupIPv6(ctx context.Context, host string) ([]netip.Addr, error) {
	if privateHost(host) {
		return nil, resolver.ErrIPNotFound
	}
	return r.Resolver.LookupIPv6(ctx, host)
}
func (r *meshResolver) ResolveECH(ctx context.Context, host string) ([]byte, error) {
	if privateHost(host) {
		return nil, resolver.ErrIPNotFound
	}
	return r.Resolver.ResolveECH(ctx, host)
}
func (r *meshResolver) ExchangeContext(ctx context.Context, msg *D.Msg) (*D.Msg, error) {
	for _, question := range msg.Question {
		if privateHost(question.Name) {
			// A mixed/multiple-question request must not send private device
			// names to an upstream. ServeMsg rejects that unsupported shape.
			return (&meshDNSService{core: r.core}).ServeMsg(ctx, msg)
		}
	}
	return r.Resolver.ExchangeContext(ctx, msg)
}
