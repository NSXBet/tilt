package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func freeGatewayPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestGatewayListenerDisabledByDefault(t *testing.T) {
	ln, err := gatewayListenerWithIO("localhost", 0, &bytes.Buffer{}, &bytes.Buffer{})
	require.NoError(t, err)
	require.Nil(t, ln)
}

func TestGatewayListenerUnprivileged(t *testing.T) {
	port := freeGatewayPort(t)
	out := &bytes.Buffer{}
	ln, err := gatewayListenerWithIO("127.0.0.1", GatewayPort(port), &bytes.Buffer{}, out)
	require.NoError(t, err)
	require.NotNil(t, ln)
	defer ln.Close()
	require.Equal(t, port, ln.Addr().(*net.TCPAddr).Port)
	require.Empty(t, out.String())
}

func TestGatewayListenerAddrInUseIsFatal(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer held.Close()
	port := held.Addr().(*net.TCPAddr).Port

	ln, err := gatewayListenerWithIO("127.0.0.1", GatewayPort(port), &bytes.Buffer{}, &bytes.Buffer{})
	require.Error(t, err)
	require.Nil(t, ln)
	require.Contains(t, err.Error(), "already in use")
}

// A privileged port without a terminal must decline without blocking — the
// fail-soft path that keeps `tilt up` usable in CI and scripts.
func TestGatewayListenerPrivilegedPortNonTTY(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("port permission errors work differently on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root; privileged ports bind without sudo")
	}

	out := &bytes.Buffer{}
	ln, err := gatewayListenerWithIO("localhost", 1, &bytes.Buffer{}, out)
	require.NoError(t, err)
	require.Nil(t, ln)
	require.Contains(t, out.String(), "not a terminal")
}

func TestAskGatewaySudoAnswers(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		isTTY bool
		want  bool
	}{
		{"yes", "y\n", true, true},
		{"yes word", "yes\n", true, true},
		{"yes uppercase", "Y\n", true, true},
		{"no", "n\n", true, false},
		{"garbage", "blue\n", true, false},
		{"eof", "", true, false},
		{"non tty skipped", "y\n", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := &bytes.Buffer{}
			in.WriteString(tc.input)
			out := &bytes.Buffer{}
			got := askGatewaySudo(in, out, tc.isTTY, "localhost", 443)
			require.Equal(t, tc.want, got)
		})
	}
}

// Full bind-and-drop round trip without sudo: the helper side passes a
// bound TCP listener's fd over a Unix socket; the parent reconstructs a
// serving listener from it.
func TestGatewayBindFDRoundTrip(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "fd.sock")

	tcpLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer tcpLn.Close()

	unixLn, err := net.Listen("unix", sock)
	require.NoError(t, err)
	defer unixLn.Close()

	sendErr := make(chan error, 1)
	go func() { sendErr <- sendListenerFD(tcpLn, sock) }()

	conn, err := unixLn.Accept()
	require.NoError(t, err)
	ln, err := recvListenerFromConn(conn)
	_ = conn.Close()
	require.NoError(t, err)
	require.NoError(t, <-sendErr)
	defer ln.Close()

	require.Equal(t, tcpLn.Addr().String(), ln.Addr().String())

	msg := "fd-roundtrip"
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, msg)
	}))
	// httptest serves on its own listener; swap in the received one.
	srv.Listener = ln
	srv.Start()
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, msg, string(body))
}

// The helper process entry point binds and hands off; verify it end-to-end
// through RunGatewayBind exactly as the sudo'd subprocess would run it.
func TestRunGatewayBindHandsOff(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "fd.sock")
	port := freeGatewayPort(t)

	unixLn, err := net.Listen("unix", sock)
	require.NoError(t, err)
	defer unixLn.Close()

	bindErr := make(chan error, 1)
	go func() { bindErr <- RunGatewayBind(context.Background(), "127.0.0.1", GatewayPort(port), sock) }()

	conn, err := unixLn.Accept()
	require.NoError(t, err)
	ln, err := recvListenerFromConn(conn)
	_ = conn.Close()
	require.NoError(t, err)
	defer ln.Close()
	require.NoError(t, <-bindErr)

	require.Equal(t, port, ln.Addr().(*net.TCPAddr).Port)
}
