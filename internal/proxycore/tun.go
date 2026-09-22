package proxycore

import (
	"context"
	"errors"
	"io"
	"net/netip"
	"runtime"
	"sync"
	"syscall"
	"time"

	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/listener/sing"
	"github.com/metacubex/mihomo/listener/sing_tun"
	"github.com/metacubex/mihomo/log"
	T "github.com/metacubex/sing-tun"
)

type tunRuntime struct {
	once   sync.Once
	device T.Tun
	stack  T.Stack
}

func (t *tunRuntime) Close() error {
	var err error
	t.once.Do(func() { err = errors.Join(t.device.Close(), t.stack.Close()) })
	return err
}

// The only kernel TUN is provided by Android's VpnService. It is consumed exactly
// once. The mesh's WireGuard network uses a userspace TUN and needs no second VPN.
func startTUN(fd int, entry C.Tunnel) (runtimeTun io.Closer, err error) {
	owned := true
	defer func() {
		if owned {
			syscall.Close(fd)
		}
	}()
	if runtime.GOOS != "android" {
		return nil, errors.New("external VPN TUN is supported on Android; use TUNFD=-1 for desktop core")
	}
	if !T.WithGVisor {
		return nil, errors.New("Android core must be built with tags cmfa,with_gvisor")
	}
	handler, err := sing.NewListenerHandler(sing.ListenerConfig{Tunnel: entry, Type: C.TUN})
	if err != nil {
		return nil, err
	}
	addresses := []netip.Prefix{netip.MustParsePrefix("172.19.0.1/30")}
	addresses6 := []netip.Prefix{netip.MustParsePrefix("fdfe:dcba:9876::1/126")}
	dnsHandler := &sing_tun.ListenerHandler{ListenerHandler: handler, Inet4Address: addresses, Inet6Address: addresses6,
		DnsAddrPorts: []netip.AddrPort{netip.MustParseAddrPort("0.0.0.0:53")}, DisableICMPForwarding: true}
	options := T.Options{Name: "jungo", FileDescriptor: fd, MTU: 1280, Inet4Address: addresses, Inet6Address: addresses6}
	device, err := T.New(options)
	if err != nil {
		return nil, err
	}
	owned = false // T.New now owns fd and closes it through device.Close.
	defer func() {
		if err != nil {
			device.Close()
		}
	}()
	stack, err := T.NewStack("gvisor", T.StackOptions{Context: context.Background(), Tun: device, TunOptions: options, Handler: dnsHandler, Logger: log.SingLogger, UDPTimeout: 3 * time.Minute, ICMPTimeout: 10 * time.Second})
	if err != nil {
		return nil, err
	}
	if err = stack.Start(); err != nil {
		stack.Close()
		return nil, err
	}
	return &tunRuntime{device: device, stack: stack}, nil
}
