package testenv

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/halpworld/halpcheers/server/internal/auth/pow"
	"github.com/halpworld/halpcheers/server/internal/config"
	"github.com/halpworld/halpcheers/server/internal/core"
	internalhttp "github.com/halpworld/halpcheers/server/internal/http"
	"github.com/halpworld/halpcheers/server/internal/obs"
	"github.com/halpworld/halpcheers/server/internal/store"
)

// Type aliases to avoid exposing internal packages directly to tests
type AccountID = core.AccountID
type Handle = core.Handle
type HandleKind = core.HandleKind

const (
	HandleKindPersonal = core.HandleKindPersonal
	HandleKindSocial   = core.HandleKindSocial
	HandleKindGroup    = core.HandleKindGroup
	HandleKindStream   = core.HandleKindStream
)

// Environment represents an assembled running Halp instance for testing and load verification.
type Environment struct {
	BaseURL    string
	DBPath     string
	Store      *store.Store
	Assembly   *internalhttp.Assembly
	Metrics    *obs.Metrics
	Config     *config.Config
	HTTPClient *http.Client

	listener net.Listener
	server   *http.Server
	tempDir  string
	cancel   context.CancelFunc
}

// Options configures a test Environment.
type Options struct {
	DBPath string
	Config *config.Config
}

// New boots an in-process fully assembled instance with SQLite in WAL mode on a random loopback port.
func New(opts ...Options) (*Environment, error) {
	ctx, cancel := context.WithCancel(context.Background())

	var tempDir string
	var dbPath string

	if len(opts) > 0 && opts[0].DBPath != "" {
		dbPath = opts[0].DBPath
	} else {
		dir, err := os.MkdirTemp("", "halp-env-*")
		if err != nil {
			cancel()
			return nil, fmt.Errorf("mkdir temp: %w", err)
		}
		tempDir = dir
		dbPath = filepath.Join(dir, "halp.db")
	}

	var cfg *config.Config
	if len(opts) > 0 && opts[0].Config != nil {
		cfg = opts[0].Config
	} else {
		cfg = config.NewDefault()
	}
	cfg.Startup.DBPath = dbPath

	st, err := store.Open(ctx, dbPath)
	if err != nil {
		cancel()
		if tempDir != "" {
			_ = os.RemoveAll(tempDir)
		}
		return nil, fmt.Errorf("open store: %w", err)
	}

	metrics := obs.NewMetrics()
	deps, assembly, err := internalhttp.AssembleDependencies(ctx, cfg, st, metrics)
	if err != nil {
		cancel()
		_ = st.Close()
		if tempDir != "" {
			_ = os.RemoveAll(tempDir)
		}
		return nil, fmt.Errorf("assemble dependencies: %w", err)
	}
	assembly.Start(ctx)

	router := internalhttp.NewRouter(deps)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		cancel()
		assembly.Close()
		_ = st.Close()
		if tempDir != "" {
			_ = os.RemoveAll(tempDir)
		}
		return nil, fmt.Errorf("listen: %w", err)
	}

	srv := &http.Server{
		Handler: router,
	}

	go func() {
		_ = srv.Serve(ln)
	}()

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        50000,
			MaxIdleConnsPerHost: 20000,
			IdleConnTimeout:     90 * time.Second,
			DialContext: (&net.Dialer{
				Timeout:   2 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
		},
	}

	env := &Environment{
		BaseURL:    fmt.Sprintf("http://%s", ln.Addr().String()),
		DBPath:     dbPath,
		Store:      st,
		Assembly:   assembly,
		Metrics:    metrics,
		Config:     cfg,
		HTTPClient: client,
		listener:   ln,
		server:     srv,
		tempDir:    tempDir,
		cancel:     cancel,
	}

	return env, nil
}

// Close gracefully terminates the running environment.
func (e *Environment) Close() error {
	e.cancel()
	_ = e.server.Close()
	_ = e.listener.Close()
	e.Assembly.Close()
	_ = e.Store.Close()
	if e.tempDir != "" {
		_ = os.RemoveAll(e.tempDir)
	}
	return nil
}

// SolvePoW generates a valid X-Halp-PoW header string (<epoch>.<nonce>) for the given target.
func (e *Environment) SolvePoW(target string, difficulty uint8) string {
	epoch := uint64(time.Now().Unix() / 300)
	var challenge [32]byte
	if e.Assembly != nil && e.Assembly.PoW != nil {
		challenge = e.Assembly.PoW.Challenge(epoch)
	}
	nonce, ok := pow.SolvePoW(target, epoch, challenge, difficulty, 2_000_000)
	if !ok {
		nonce = "1"
	}
	return fmt.Sprintf("%d.%s", epoch, nonce)
}

// CreateAccountAndSession registers an account and returns accountID and bearer session token.
func (e *Environment) CreateAccountAndSession(seed byte) (core.AccountID, string, error) {
	ctx := context.Background()
	keyHash := bytes.Repeat([]byte{seed}, 32)
	id, err := e.Store.CreateAccount(ctx, keyHash, "eu-1")
	if err != nil {
		return 0, "", err
	}
	sess, err := e.Assembly.Auth.Sessions().CreateSession(id)
	if err != nil {
		return 0, "", err
	}
	return id, sess.Token, nil
}

// CreateHandle registers a handle for an account.
func (e *Environment) CreateHandle(accountID core.AccountID, handle core.Handle, kind core.HandleKind, label string) error {
	ctx := context.Background()
	return e.Store.CreateHandle(ctx, handle, accountID, kind, label)
}

// RecordBlock directly inserts a row into the blocks table for testing.
func (e *Environment) RecordBlock(senderID core.AccountID, handle core.Handle) error {
	ctx := context.Background()
	return e.Store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO blocks (sender_account_id, handle, created_day)
			VALUES (?, ?, ?)
		`, senderID.Int64(), handle.Raw(), core.Today().Int())
		return err
	})
}
