package control

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"wechatbox/internal/config"
)

var (
	// ErrUnavailable means no running control server could be reached.
	ErrUnavailable = errors.New("control server unavailable")
	// ErrAlreadyRunning means another live wechatbox run process owns the socket.
	ErrAlreadyRunning = errors.New("wechatbox run already active")

	socketActive = defaultSocketActive
)

// Server exposes the local control API over a Unix domain socket.
type Server struct {
	httpServer *http.Server
	socketPath string
	done       chan error
}

// StartServer starts the control server at the default socket path.
func StartServer(ctx context.Context, reload func(context.Context) error) (*Server, error) {
	path, err := config.ControlSocketPath()
	if err != nil {
		return nil, err
	}
	return StartServerAt(ctx, path, reload)
}

// StartServerAt starts the control server at socketPath.
func StartServerAt(ctx context.Context, socketPath string, reload func(context.Context) error) (*Server, error) {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0700); err != nil {
		return nil, fmt.Errorf("create control socket dir: %w", err)
	}
	if err := prepareSocket(socketPath); err != nil {
		return nil, err
	}

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("listen control socket: %w", err)
	}
	if err := os.Chmod(socketPath, 0600); err != nil {
		ln.Close()
		os.Remove(socketPath)
		return nil, fmt.Errorf("chmod control socket: %w", err)
	}

	s := &Server{
		httpServer: &http.Server{Handler: newHandler(reload)},
		socketPath: socketPath,
		done:       make(chan error, 1),
	}

	go func() {
		err := s.httpServer.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		s.done <- err
	}()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Close(shutdownCtx)
	}()

	return s, nil
}

func newHandler(reload func(context.Context) error) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/reload", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if err := reload(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

// Close shuts down the control server and removes its socket.
func (s *Server) Close(ctx context.Context) error {
	err := s.httpServer.Shutdown(ctx)
	os.Remove(s.socketPath)
	select {
	case serveErr := <-s.done:
		if err == nil {
			err = serveErr
		}
	default:
	}
	return err
}

// NotifyReload asks a running wechatbox process to reload accounts.
func NotifyReload(ctx context.Context) error {
	path, err := config.ControlSocketPath()
	if err != nil {
		return err
	}
	return NotifyReloadAt(ctx, path)
}

// NotifyReloadAt asks the control server at socketPath to reload accounts.
func NotifyReloadAt(ctx context.Context, socketPath string) error {
	if _, err := os.Stat(socketPath); err != nil {
		if os.IsNotExist(err) {
			return ErrUnavailable
		}
		return fmt.Errorf("stat control socket: %w", err)
	}

	client := unixHTTPClient(socketPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://wechatbox/reload", nil)
	if err != nil {
		return err
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("reload request failed: HTTP %d", resp.StatusCode)
	}
	return nil
}

func prepareSocket(socketPath string) error {
	if _, err := os.Stat(socketPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat control socket: %w", err)
	}
	if socketActive(socketPath) {
		return ErrAlreadyRunning
	}
	if err := os.Remove(socketPath); err != nil {
		return fmt.Errorf("remove stale control socket: %w", err)
	}
	return nil
}

func defaultSocketActive(socketPath string) bool {
	conn, err := net.DialTimeout("unix", socketPath, 200*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func unixHTTPClient(socketPath string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
		Timeout: 5 * time.Second,
	}
}
