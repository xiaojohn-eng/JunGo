// Package mobile is the gomobile API. It exposes only supported binding types.
package mobile

import "github.com/xiaojohn-eng/JunGo/internal/engine"

type Platform interface {
	Protect(fd int) bool
	OwnerUID(protocol int, localIP string, localPort int, remoteIP string, remotePort int) int
	OpenURI(uri, mode string) int
	URIInfo(uri string) string
}
type Engine struct{ runtime *engine.Engine }

func NewEngine(stateDir string, platform Platform) (*Engine, error) {
	runtime, err := engine.New(stateDir, platform)
	if err != nil {
		return nil, err
	}
	return &Engine{runtime}, nil
}
func (e *Engine) Request(request string) (string, error) { return e.runtime.Request(request) }
func (e *Engine) StartVPN(fd int) error                  { return e.runtime.StartVPN(fd) }
func (e *Engine) StopVPN()                               { e.runtime.StopVPN() }
func (e *Engine) Close() error                           { return e.runtime.Close() }
