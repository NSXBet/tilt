package server

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"github.com/tilt-dev/tilt/pkg/model"
)

// GatewayPort is the port of the extra opt-in gateway listener
// (--gateway-port). Distinct from model.WebPort so wire can bind both.
type GatewayPort int

// GatewayListener serves the Tilt web handler — the worktree gateway
// (<wt>.tilt.localhost) included — on the extra opt-in port. It is nil when
// the feature is off (port 0) or the privileged bind was declined: tilt up
// never requires sudo for the gateway.
type GatewayListener net.Listener

// ProvideGatewayListener opens the opt-in gateway port.
//
// Port 0 (the default) disables the feature entirely. Binding a privileged
// port (below 1024) fails with EACCES/EPERM for an unprivileged process; in
// that case Tilt asks once to run a one-shot bind helper under sudo. The
// helper only opens the listening socket and passes the file descriptor back
// to this process (SCM_RIGHTS), then exits — nothing keeps running as root,
// and all serving stays in the unprivileged Tilt process. Declining the
// prompt (or any other sudo failure) logs a warning and disables the
// gateway port for this run: tilt up itself continues normally.
func ProvideGatewayListener(host model.WebHost, port GatewayPort) (GatewayListener, error) {
	return gatewayListenerWithIO(host, port, os.Stdin, os.Stdout)
}

func gatewayListenerWithIO(host model.WebHost, port GatewayPort, stdin io.Reader, stdout io.Writer) (GatewayListener, error) {
	if port == 0 {
		return nil, nil
	}

	ln, err := net.Listen("tcp", net.JoinHostPort(string(host), strconv.Itoa(int(port))))
	if err == nil {
		return GatewayListener(ln), nil
	}

	if !errors.Is(err, syscall.EACCES) && !errors.Is(err, syscall.EPERM) {
		if errors.Is(err, syscall.EADDRINUSE) || strings.Contains(err.Error(), "address already in use") {
			return nil, fmt.Errorf("the gateway port %d is already in use; pick another with --gateway-port (original error: %v)", port, err)
		}
		return nil, fmt.Errorf("binding gateway port %d: %v", port, err)
	}

	// Privileged port: needs a one-shot sudo bind-and-drop.
	isTTY := false
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		isTTY = true
	}
	if !askGatewaySudo(stdin, stdout, isTTY, host, port) {
		fmt.Fprintf(stdout, "Continuing without the gateway port: tilt up is unaffected, "+
			"but %s:<port> gateway URLs need the extra listener.\n", host)
		return nil, nil
	}

	ln, err = bindViaSudo(host, port)
	if err != nil {
		fmt.Fprintf(stdout, "Continuing without the gateway port: the sudo bind helper failed: %v\n", err)
		return nil, nil
	}
	return GatewayListener(ln), nil
}

// askGatewaySudo asks the user to elevate for the gateway bind. Only
// interactive terminals get a prompt; anything else (pipes, CI) declines
// without blocking.
func askGatewaySudo(stdin io.Reader, stdout io.Writer, isTTY bool, host model.WebHost, port GatewayPort) bool {
	if !isTTY {
		fmt.Fprintf(stdout, "Gateway port %d requires admin to bind, but stdin is not a terminal; "+
			"re-run from a terminal to allow it.\n", port)
		return false
	}

	fmt.Fprintf(stdout, "The gateway port %d on %s is privileged and needs admin to bind.\n"+
		"Tilt will run a one-shot helper under sudo that only opens the socket and hands it back; "+
		"nothing keeps running as root.\n", port, host)
	fmt.Fprint(stdout, "Open the gateway port for this run with sudo? (y/n) ")

	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil {
		fmt.Fprintln(stdout)
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

// bindViaSudo runs the one-shot helper (tilt gateway-bind) under sudo and
// receives the bound listener over a private Unix socket.
func bindViaSudo(host model.WebHost, port GatewayPort) (net.Listener, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locating the tilt binary: %v", err)
	}

	dir, err := os.MkdirTemp("", "tilt-gateway-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "fd.sock")

	cmd := exec.Command("sudo", "--", exe, "gateway-bind",
		"--host", string(host), "--port", strconv.Itoa(int(port)), "--socket", sock)
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr
	unixLn, err := net.Listen("unix", sock)
	if err != nil {
		return nil, err
	}
	defer unixLn.Close()

	errCh := make(chan error, 1)
	go func() { errCh <- cmd.Run() }()

	type bindResult struct {
		ln  net.Listener
		err error
	}
	accCh := make(chan bindResult, 1)
	go func() {
		conn, err := unixLn.Accept()
		if err != nil {
			accCh <- bindResult{err: err}
			return
		}
		ln, err := recvListenerFromConn(conn)
		_ = conn.Close()
		accCh <- bindResult{ln: ln, err: err}
	}()

	if runErr := <-errCh; runErr != nil {
		return nil, fmt.Errorf("sudo bind helper: %v", runErr)
	}
	res := <-accCh
	if res.err != nil {
		return nil, fmt.Errorf("receiving gateway listener fd: %v", res.err)
	}
	return res.ln, nil
}

// RunGatewayBind is the helper's body: bind the privileged socket and hand
// the file descriptor to the waiting parent. It never serves traffic and
// exits immediately after the handoff.
func RunGatewayBind(ctx context.Context, host model.WebHost, port GatewayPort, socketPath string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(string(host), strconv.Itoa(int(port))))
	if err != nil {
		return err
	}
	return sendListenerFD(ln, socketPath)
}

// sendListenerFD dials the parent's Unix socket and passes the listener's
// file descriptor over it (SCM_RIGHTS).
func sendListenerFD(ln net.Listener, socketPath string) error {
	tcp, ok := ln.(*net.TCPListener)
	if !ok {
		return fmt.Errorf("gateway bind helper requires a TCP listener, got %T", ln)
	}
	file, err := tcp.File()
	if err != nil {
		return err
	}
	defer file.Close()

	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return fmt.Errorf("expected a Unix connection, got %T", conn)
	}

	rights := unix.UnixRights(int(file.Fd()))
	rc, err := uc.SyscallConn()
	if err != nil {
		return err
	}
	var sendErr error
	if ctlErr := rc.Control(func(fd uintptr) {
		sendErr = unix.Sendmsg(int(fd), nil, rights, nil, 0)
	}); ctlErr != nil {
		return ctlErr
	}
	return sendErr
}

// recvListenerFromConn reconstructs a listener from the file descriptor the
// helper passed over the given Unix connection.
func recvListenerFromConn(conn net.Conn) (net.Listener, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return nil, fmt.Errorf("expected a Unix connection, got %T", conn)
	}

	buf := make([]byte, 32)
	oob := make([]byte, 64)
	n, oobn, _, _, err := uc.ReadMsgUnix(buf, oob)
	if err != nil {
		return nil, fmt.Errorf("receiving gateway listener fd: %v", err)
	}
	_ = n
	msgs, err := unix.ParseSocketControlMessage(oob[:oobn])
	if err != nil {
		return nil, err
	}
	if len(msgs) != 1 {
		return nil, fmt.Errorf("gateway bind helper sent %d control messages, want 1", len(msgs))
	}
	fds, err := unix.ParseUnixRights(&msgs[0])
	if err != nil {
		return nil, err
	}
	if len(fds) != 1 {
		return nil, fmt.Errorf("gateway bind helper sent %d fds, want 1", len(fds))
	}

	file := os.NewFile(uintptr(fds[0]), "gateway-listener")
	defer file.Close()
	return net.FileListener(file)
}
