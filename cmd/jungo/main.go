package main

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/xiaojohn-eng/JunGo/internal/control"
	"github.com/xiaojohn-eng/JunGo/internal/engine"
	"github.com/xiaojohn-eng/JunGo/internal/mesh"
	"github.com/xiaojohn-eng/JunGo/internal/relay"
	"github.com/xiaojohn-eng/JunGo/internal/secure"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		return errors.New("用法: jungo serve|pair-code|agent|rpc|version")
	}
	switch args[0] {
	case "version":
		fmt.Println("JunGo v0.2.3-preview")
		return nil
	case "serve":
		return serve(args[1:])
	case "pair-code":
		return pairing(args[1:])
	case "agent":
		return agent(args[1:])
	case "rpc":
		return rpc(args[1:])
	default:
		return fmt.Errorf("未知命令 %q", args[0])
	}
}
func shutdownContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
func tokenFile(dir string) (string, error) {
	path := filepath.Join(dir, "admin.token")
	b, err := os.ReadFile(path)
	if err == nil {
		return strings.TrimSpace(string(b)), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	token := secure.Random(32)
	return token, secure.WriteFile(path, []byte(token+"\n"))
}
func serve(args []string) error {
	f := flag.NewFlagSet("serve", flag.ContinueOnError)
	dir := f.String("state", ".state/control", "private persistent state directory")
	addr := f.String("listen", "127.0.0.1:9443", "HTTPS address; use :9443 for deployment")
	stun := f.String("stun", "127.0.0.1:3478", "UDP STUN address; empty to disable")
	if err := f.Parse(args); err != nil {
		return err
	}
	abs, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	cert, fp, err := secure.Certificate(filepath.Join(abs, "tls"))
	if err != nil {
		return err
	}
	admin, err := tokenFile(abs)
	if err != nil {
		return err
	}
	store, err := control.NewStore(filepath.Join(abs, "registry.json"))
	if err != nil {
		return err
	}
	handler, err := control.NewHandler(store, control.Config{AdminToken: admin})
	if err != nil {
		return err
	}
	relayServer, err := relay.NewHandler(store, relay.Config{})
	if err != nil {
		return err
	}
	defer relayServer.Close()
	mux := http.NewServeMux()
	mux.Handle("/v1/relay", relayServer)
	mux.Handle("/", handler)
	server := &http.Server{Addr: *addr, Handler: mux, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}}, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32 << 10}
	ctx, cancel := shutdownContext()
	defer cancel()
	if *stun != "" {
		udp, err := net.ListenPacket("udp", *stun)
		if err != nil {
			return err
		}
		defer udp.Close()
		go func() { _ = mesh.ServeSTUN(udp) }()
	}
	errorsCh := make(chan error, 1)
	go func() { errorsCh <- server.ListenAndServeTLS("", "") }()
	log.Printf("control HTTPS %s; service ID %s; SHA256 %s; administrator token stored privately at %s", *addr, store.ServiceID(), fp, filepath.Join(abs, "admin.token"))
	select {
	case err := <-errorsCh:
		return err
	case <-ctx.Done():
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(ctx)
	}
}
func pairing(args []string) error {
	f := flag.NewFlagSet("pair-code", flag.ContinueOnError)
	dir := f.String("state", ".state/control", "control state directory")
	server := f.String("server", "https://127.0.0.1:9443", "control URL, included in QR payload")
	if err := f.Parse(args); err != nil {
		return err
	}
	_, fp, err := secure.Certificate(filepath.Join(*dir, "tls"))
	if err != nil {
		return err
	}
	b, err := os.ReadFile(filepath.Join(*dir, "admin.token"))
	if err != nil {
		return err
	}
	cfg, err := secure.PinnedTLS(fp)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", strings.TrimRight(*server, "/")+"/v1/pairing-codes", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(b)))
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect rejected") }}
	defer client.CloseIdleConnections()
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 201 {
		return fmt.Errorf("创建配对码失败 HTTP%d", response.StatusCode)
	}
	var code control.PairingCode
	if err = json.NewDecoder(response.Body).Decode(&code); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"server": *server, "fingerprint": fp, "serviceId": code.ServiceID, "code": code.Code, "expiresAt": code.ExpiresAt})
}

type localAPI struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

func agent(args []string) (resultErr error) {
	f := flag.NewFlagSet("agent", flag.ContinueOnError)
	dir := f.String("state", ".state/device", "device state directory")
	local := f.Bool("local-api", false, "enable authenticated loopback RPC for desktop UI/CLI")
	if err := f.Parse(args); err != nil {
		return err
	}
	abs, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(abs, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(abs, "agent.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("该状态目录已有一个后台程序运行")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	runtime, err := engine.New(abs, nil)
	if err != nil {
		return err
	}
	// A failed final checkpoint must be visible before an operator downgrades;
	// the durable journal remains authoritative until checkpoint succeeds.
	defer func() { resultErr = errors.Join(resultErr, runtime.Close()) }()
	ctx, cancel := shutdownContext()
	defer cancel()
	var server *http.Server
	if *local {
		l, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return err
		}
		defer l.Close()
		token := secure.Random(32)
		mux := http.NewServeMux()
		mux.HandleFunc("POST /v1/rpc", func(w http.ResponseWriter, r *http.Request) {
			supplied := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(supplied), []byte(token)) != 1 || r.Header.Get("Origin") != "" {
				http.Error(w, "unauthorized", 401)
				return
			}
			b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 10<<20))
			if err != nil {
				http.Error(w, "request too large", 413)
				return
			}
			result, err := runtime.Request(string(b))
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			if err != nil {
				w.WriteHeader(400)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			_, _ = io.WriteString(w, result)
		})
		server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
		info, _ := json.Marshal(localAPI{URL: "http://" + l.Addr().String(), Token: token})
		apiPath := filepath.Join(abs, "local-api.json")
		if err = secure.WriteFile(apiPath, info); err != nil {
			return err
		}
		defer os.Remove(apiPath)
		go func() {
			if err := server.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Print(err)
				cancel()
			}
		}()
	}
	<-ctx.Done()
	if server != nil {
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(stop)
	}
	return nil
}
func rpc(args []string) error {
	f := flag.NewFlagSet("rpc", flag.ContinueOnError)
	dir := f.String("state", ".state/device", "device state directory")
	if err := f.Parse(args); err != nil {
		return err
	}
	var info localAPI
	b, err := os.ReadFile(filepath.Join(*dir, "local-api.json"))
	if err != nil {
		return err
	}
	if err = json.Unmarshal(b, &info); err != nil {
		return err
	}
	endpoint, err := localRPCURL(info.URL)
	if err != nil {
		return err
	}
	request, err := io.ReadAll(io.LimitReader(os.Stdin, 10<<20))
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", endpoint, strings.NewReader(string(request)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+info.Token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect rejected") }}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, err = io.Copy(os.Stdout, response.Body)
	if response.StatusCode != 200 {
		return fmt.Errorf("RPC返回HTTP%d", response.StatusCode)
	}
	return err
}

// Validate parsed authority, not a string prefix: userinfo can disguise a remote
// host as http://127.0.0.1:1234@remote and disclose the local capability token.
func localRPCURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("local API must use an HTTP loopback address")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return "", errors.New("invalid local API port")
	}
	u.Path = "/v1/rpc"
	return u.String(), nil
}
