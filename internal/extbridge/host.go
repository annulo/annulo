package extbridge

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// IsHostInvocation：Chrome 拉起 native host 时第一个参数是调用方插件的 origin（chrome-extension://<id>/），
// Windows 上后面还跟着 --parent-window=…。annulo 程序看到它就当 native host 跑（RunHost），不进命令行。
func IsHostInvocation(args []string) bool {
	return len(args) > 1 && strings.HasPrefix(args[1], "chrome-extension://")
}

// RunHost 是 native host：stdin / stdout 上是 Chrome 的 native messaging（每条前面 4 字节小端长度 + JSON），
// 一条条转给本机 Annulo 的 WebSocket（/_annulo/api/browser/ext），反过来也一样。
// Annulo 没开、重启了：每 2 秒重连一次；Chrome 关了 stdin 就退出。addr 每次重连前重新读（Annulo 换过端口也能连上）。
func RunHost(origin string, addr func() string, header http.Header) error {
	id := strings.TrimSuffix(strings.TrimPrefix(origin, "chrome-extension://"), "/")
	known := false
	for _, x := range ExtensionIDs {
		known = known || x == id
	}
	if !known {
		return fmt.Errorf("unknown extension %q", origin)
	}
	in := bufio.NewReader(os.Stdin)
	var outMu sync.Mutex
	writeOut := func(b []byte) error {
		// Chrome 收的单条消息最多 1MB
		if len(b) > 1<<20 {
			return errors.New("message to the extension is over 1MB")
		}
		outMu.Lock()
		defer outMu.Unlock()
		var n [4]byte
		binary.LittleEndian.PutUint32(n[:], uint32(len(b)))
		if _, err := os.Stdout.Write(n[:]); err != nil {
			return err
		}
		_, err := os.Stdout.Write(b)
		return err
	}

	var mu sync.Mutex
	var ws *websocket.Conn
	// 从 Chrome 读：插件的回答和事件，转给 Annulo；没连着就丢（Annulo 那边的请求反正也已经失败了）
	stdinDone := make(chan error, 1)
	go func() {
		for {
			var n [4]byte
			if _, err := io.ReadFull(in, n[:]); err != nil {
				stdinDone <- err
				return
			}
			size := binary.LittleEndian.Uint32(n[:])
			if size > 64<<20 {
				stdinDone <- errors.New("message from the extension is too large")
				return
			}
			b := make([]byte, size)
			if _, err := io.ReadFull(in, b); err != nil {
				stdinDone <- err
				return
			}
			mu.Lock()
			c := ws
			mu.Unlock()
			if c != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				c.Write(ctx, websocket.MessageText, b)
				cancel()
			}
		}
	}()

	for {
		select {
		case err := <-stdinDone:
			if err == io.EOF {
				return nil
			}
			return err
		default:
		}
		u := url.URL{Scheme: "ws", Host: addr(), Path: "/_annulo/api/browser/ext", RawQuery: url.Values{"ext": {id}}.Encode()}
		dctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		c, _, err := websocket.Dial(dctx, u.String(), &websocket.DialOptions{HTTPHeader: header})
		cancel()
		if err != nil {
			select {
			case err := <-stdinDone:
				if err == io.EOF {
					return nil
				}
				return err
			case <-time.After(2 * time.Second):
			}
			continue
		}
		c.SetReadLimit(1 << 20)
		mu.Lock()
		ws = c
		mu.Unlock()
		// 从 Annulo 读：请求，转给插件
		readDone := make(chan struct{})
		go func() {
			defer close(readDone)
			for {
				_, b, err := c.Read(context.Background())
				if err != nil {
					return
				}
				if err := writeOut(b); err != nil {
					return
				}
			}
		}()
		select {
		case err := <-stdinDone:
			c.Close(websocket.StatusNormalClosure, "chrome closed")
			if err == io.EOF {
				return nil
			}
			return err
		case <-readDone:
		}
		mu.Lock()
		ws = nil
		mu.Unlock()
		c.CloseNow()
	}
}
