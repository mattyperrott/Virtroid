package httpapi

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A slow upload outlives the server's ReadTimeout only on a route that
// extends its deadlines, as the sealed import does.
func TestExtendDeadlinesLetsASlowUploadFinish(t *testing.T) {
	for _, extend := range []bool{false, true} {
		read := make(chan error, 1)
		server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if extend {
				extendDeadlines(w, 5*time.Second, 5*time.Second)
			}
			_, err := io.ReadAll(r.Body)
			read <- err
		}))
		server.Config.ReadTimeout = 300 * time.Millisecond
		server.Start()

		conn, err := net.Dial("tcp", server.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		body := strings.Repeat("x", 8)
		_, _ = io.WriteString(conn, "POST / HTTP/1.1\r\nHost: test\r\nContent-Length: 8\r\n\r\n")
		for i := 0; i < len(body); i++ {
			time.Sleep(100 * time.Millisecond)
			if _, err := io.WriteString(conn, body[i:i+1]); err != nil {
				break
			}
		}
		var got error
		select {
		case got = <-read:
		case <-time.After(5 * time.Second):
			t.Fatal("handler never finished reading")
		}
		_ = conn.Close()
		server.Close()
		if extend && got != nil {
			t.Fatalf("extended route failed to read a slow upload: %v", got)
		}
		if !extend && got == nil {
			t.Fatal("a slow upload outlived ReadTimeout without an extended deadline; the test proves nothing")
		}
	}
}
