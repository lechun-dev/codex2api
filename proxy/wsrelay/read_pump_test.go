package wsrelay

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// dialTestConn 建一条到测试服务器的池化连接（走 NewWsConnection，含 read pump）。
func dialTestConn(t *testing.T, manager *Manager, serverURL string) (*WsConnection, string) {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(serverURL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	key := manager.poolKey(1, wsURL, "session-1", "")
	session := NewSession(1, manager)
	session.SetConnected(true)
	wc := NewWsConnection(conn, session, wsURL)
	wc.PoolKey = key
	manager.connections.Store(key, wc)
	manager.sessions.Store(key, session)
	return wc, key
}

// TestProbeDetectsPendingCloseFrame 复现 issue #349 的最小场景：
// 上游对空闲连接发送 Close 1011 (keepalive ping timeout) 且底层 TCP 仍保持打开。
// 旧实现只写 Ping 探活会误判存活，下一次 ReadMessage 立即暴雷；常驻 read pump
// 应在 Close 帧到达时就翻掉本地状态，probe 必须返回 false。
func TestProbeDetectsPendingCloseFrame(t *testing.T) {
	holdConn := make(chan struct{})
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		// 模拟上游 keepalive 超时判死：发 Close 1011 但暂不关 TCP
		_ = conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "keepalive ping timeout"),
			time.Now().Add(time.Second),
		)
		<-holdConn
		conn.Close()
	}))
	defer server.Close()
	defer close(holdConn)

	manager := NewManager()
	t.Cleanup(manager.Stop)
	wc, key := dialTestConn(t, manager, server.URL)

	// 等 pump 消化 Close 帧（到达即翻状态，无需真实等待上游超时）
	deadline := time.Now().Add(2 * time.Second)
	for wc.IsConnected() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if wc.IsConnected() {
		t.Fatal("read pump should flip connection state when Close 1011 arrives while idle")
	}
	if probeConnection(wc) {
		t.Fatal("probeConnection must report a connection with a received Close frame as dead (issue #349)")
	}
	if err := wc.getReadErr(); err == nil || !strings.Contains(err.Error(), "keepalive ping timeout") {
		t.Fatalf("readErr = %v, want close 1011 keepalive ping timeout", err)
	}

	// acquire 路径应把它当死连接清理，而不是分配给下一个请求
	manager.DiscardConnection(wc)
	if _, ok := manager.connections.Load(key); ok {
		t.Fatal("dead connection must not remain in pool")
	}
}

// TestIdleConnectionAutoRepliesPong 验证空闲连接（无业务 reader）也能实时回应
// 上游 Ping：常驻 pump 处理控制帧并回 Pong。旧实现空闲期无 reader，上游 Ping
// 石沉大海，等来的只有 keepalive 超时判死。
func TestIdleConnectionAutoRepliesPong(t *testing.T) {
	var pongs atomic.Int32
	gotPong := make(chan struct{}, 4)
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer conn.Close()
		conn.SetPongHandler(func(string) error {
			pongs.Add(1)
			select {
			case gotPong <- struct{}{}:
			default:
			}
			return nil
		})
		// 模拟上游 keepalive：连续 Ping，同时保持读循环以处理 Pong
		go func() {
			for i := 0; i < 3; i++ {
				if err := conn.WriteControl(websocket.PingMessage, []byte("ka"), time.Now().Add(time.Second)); err != nil {
					return
				}
				time.Sleep(50 * time.Millisecond)
			}
		}()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	manager := NewManager()
	t.Cleanup(manager.Stop)
	wc, _ := dialTestConn(t, manager, server.URL)

	select {
	case <-gotPong:
	case <-time.After(2 * time.Second):
		t.Fatal("idle connection did not reply Pong to upstream Ping within 2s (issue #349: no reader while idle)")
	}
	if !wc.IsConnected() {
		t.Fatal("connection should stay alive while answering upstream keepalive")
	}
	if !probeConnection(wc) {
		t.Fatal("healthy idle connection should pass probe")
	}
}

// TestProbeRejectsConnectionWithStrayFrame 空闲期收到业务帧 = 上一响应的残留，
// 复用会把它串给下一个用户 (issue #308)。probe 必须检出并废弃该连接。
func TestProbeRejectsConnectionWithStrayFrame(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer conn.Close()
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.output_text.delta","delta":"stale"}`))
		time.Sleep(300 * time.Millisecond)
	}))
	defer server.Close()

	manager := NewManager()
	t.Cleanup(manager.Stop)
	wc, _ := dialTestConn(t, manager, server.URL)

	// 等 pump 把残留帧送进缓冲
	deadline := time.Now().Add(2 * time.Second)
	for len(wc.frames) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if probeConnection(wc) {
		t.Fatal("probe must reject a connection with stray data frames buffered while idle")
	}
	if wc.IsConnected() {
		t.Fatal("connection with stray frames must be closed")
	}
}

// TestReadStreamStillWorksThroughPump 冒烟：经 pump 的完整请求-响应流程不回归。
func TestReadStreamStillWorksThroughPump(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer conn.Close()
		// 等请求帧再回响应，模拟真实 response.create 流程
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.output_text.delta","delta":"hello"}`))
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp_1"}}`))
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()

	manager := NewManager()
	t.Cleanup(manager.Stop)
	wc, key := dialTestConn(t, manager, server.URL)
	pr := wc.session.AddPendingRequest("session-1")

	if err := wc.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create"}`)); err != nil {
		t.Fatalf("send request: %v", err)
	}

	wsResp := &WsResponse{conn: wc, pendingReq: pr, sessionID: "session-1", manager: manager, readErrChan: make(chan error, 1)}
	var frames []string
	if err := wsResp.ReadStream(func(data []byte) bool {
		frames = append(frames, string(data))
		return true
	}); err != nil {
		t.Fatalf("ReadStream: %v", err)
	}
	if len(frames) != 2 || !strings.Contains(frames[1], "response.completed") {
		t.Fatalf("frames = %v, want delta + completed", frames)
	}

	if err := wsResp.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// 干净完成的连接应留在池中，且 probe 通过（可被下一个请求复用）
	if _, ok := manager.connections.Load(key); !ok {
		t.Fatal("cleanly completed connection should remain pooled")
	}
	if !probeConnection(wc) {
		t.Fatal("cleanly completed connection should pass probe")
	}
}
