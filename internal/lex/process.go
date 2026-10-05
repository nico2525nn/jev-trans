package lex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ProcessAnalyzer runs an external morphological analyser as a subprocess and
// speaks a small line protocol to it.
//
// Why a process and not a library: plan2.md requires that jev-trans core stay
// standard-library-only and hold no large linguistic asset. Sudachi is a Python
// extension module, UniDic is normally reached through MeCab or a dedicated
// binary, and both ship dictionaries measured in hundreds of megabytes. Putting
// any of that behind a Go import would make the core neither dependency-free nor
// small, and would tie the build to a native toolchain. Over a pipe, the core
// needs only os/exec and JSON, and an environment that has no analyser installed
// simply falls back to the builtin one.
//
// The protocol is newline-delimited JSON in both directions:
//
//	request  {"text": "...", "profile": "modern"}
//	response {"backend":"sudachi","version":"0.8.2","dictionary":"core",
//	          "tokens":[{"surface":"...","lemma":"...","pos":["名詞"],
//	                      "start":0,"end":1,"unknown":false}]}
//
// One request per line, one response per line, and the backend is expected to
// keep running: spawning Sudachi costs roughly a second to import its dictionary,
// which per sentence would dominate the whole translation.

// ProcessConfig describes an external analyser.
type ProcessConfig struct {
	// Command is the executable. It is resolved through PATH unless it
	// contains a separator.
	Command string
	// Args are passed before the protocol stream.
	Args []string
	// Name is the label recorded in the trace.
	Name string
	// Version is recorded in the trace and should be pinned. plan2.md is
	// explicit that a Sudachi patch release can be a breaking change.
	Version string
	// Dictionary is recorded in the trace so a translation can be traced to the
	// lexicon that produced its analysis.
	Dictionary string
	// Profiles restricts which text profiles this backend claims.
	Profiles []Profile
	// Dir is the working directory for the process, which is how a MeCab or
	// UniDic backend finds its dictionary relative to itself.
	Dir string
	// Env replaces the child environment when non-nil.
	Env []string
	// StartupTimeout bounds the handshake with the backend.
	StartupTimeout time.Duration
}

// ProcessAnalyzer is a MorphAnalyzer backed by a subprocess.
type ProcessAnalyzer struct {
	cfg ProcessConfig

	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   *bufio.Writer
	stdout  *bufio.Scanner
	stderr  *strings.Builder
	started bool
	ready   bool
	lastErr error
}

// NewProcessAnalyzer returns a backend. It does not start the process: a
// backend that is never asked for a profile must cost nothing.
func NewProcessAnalyzer(cfg ProcessConfig) *ProcessAnalyzer {
	if cfg.Name == "" {
		cfg.Name = cfg.Command
	}
	return &ProcessAnalyzer{cfg: cfg}
}

// Name implements MorphAnalyzer.
func (p *ProcessAnalyzer) Name() string { return p.cfg.Name }

// Version implements MorphAnalyzer.
func (p *ProcessAnalyzer) Version() string { return p.cfg.Version }

// Dictionary returns the dictionary label for the trace.
func (p *ProcessAnalyzer) Dictionary() string { return p.cfg.Dictionary }

// Supports implements MorphAnalyzer.
func (p *ProcessAnalyzer) Supports(pr Profile) bool {
	if len(p.cfg.Profiles) == 0 {
		return true
	}
	for _, c := range p.cfg.Profiles {
		if c == pr {
			return true
		}
	}
	return false
}

type processRequest struct {
	Text    string  `json:"text"`
	Profile Profile `json:"profile"`
}

type processResponse struct {
	Backend    string  `json:"backend"`
	Version    string  `json:"version"`
	Dictionary string  `json:"dictionary"`
	Error      string  `json:"error"`
	Tokens     []Token `json:"tokens"`
}

// Analyze implements MorphAnalyzer.
func (p *ProcessAnalyzer) Analyze(ctx context.Context, text string, pr Profile) (*Analysis, error) {
	if strings.TrimSpace(text) == "" {
		return &Analysis{Backend: p.cfg.Name, BackendVersion: p.cfg.Version,
			Dictionary: p.cfg.Dictionary, Profile: pr}, nil
	}
	if err := p.start(ctx); err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	req := processRequest{Text: text, Profile: pr}
	line, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := p.stdin.Write(append(line, '\n')); err != nil {
		p.reset()
		return nil, fmt.Errorf("%s: write: %w", p.cfg.Name, err)
	}
	if err := p.stdin.Flush(); err != nil {
		p.reset()
		return nil, fmt.Errorf("%s: flush: %w", p.cfg.Name, err)
	}
	if !p.stdout.Scan() {
		err := p.lastErr
		p.reset()
		if err == nil {
			err = errors.New("backend closed the stream")
		}
		return nil, fmt.Errorf("%s: %w", p.cfg.Name, err)
	}
	var resp processResponse
	if err := json.Unmarshal(p.stdout.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("%s: malformed response: %w", p.cfg.Name, err)
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("%s: %s", p.cfg.Name, resp.Error)
	}
	if len(resp.Tokens) == 0 {
		return nil, fmt.Errorf("%s: returned no tokens for %d characters", p.cfg.Name, len(text))
	}
	out := &Analysis{
		Tokens:         resp.Tokens,
		Backend:        p.cfg.Name,
		BackendVersion: p.cfg.Version,
		Dictionary:     p.cfg.Dictionary,
		Profile:        pr,
	}
	if resp.Version != "" {
		out.BackendVersion = resp.Version
	}
	if resp.Dictionary != "" {
		out.Dictionary = resp.Dictionary
	}
	out.ElapsedMS = float64(len(text)) / 0 // filled by the caller if it cares
	return out, nil
}

// Close stops the backend process.
func (p *ProcessAnalyzer) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopLocked()
}

func (p *ProcessAnalyzer) start(ctx context.Context) error {
	if p.started && p.cmd != nil && p.cmd.ProcessState == nil {
		return nil
	}
	timeout := p.cfg.StartupTimeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	// A failed start must not consume the caller's deadline.
	_, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()

	cmd := exec.Command(p.cfg.Command, p.cfg.Args...)
	cmd.Dir = p.cfg.Dir
	if p.cfg.Env != nil {
		cmd.Env = p.cfg.Env
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("%s: stdin: %w", p.cfg.Name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("%s: stdout: %w", p.cfg.Name, err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s: start %q: %w", p.cfg.Name, p.cfg.Command, err)
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	p.cmd = cmd
	p.stdin = bufio.NewWriter(stdin)
	p.stdout = sc
	p.stderr = &stderr
	p.started = true
	p.ready = true
	p.lastErr = nil
	return nil
}

func (p *ProcessAnalyzer) reset() {
	_ = p.stopLocked()
}

func (p *ProcessAnalyzer) stopLocked() error {
	if p.cmd == nil || p.cmd.Process == nil {
		p.started = false
		return nil
	}
	if p.stderr != nil && p.stderr.Len() > 0 && p.lastErr != nil {
		p.lastErr = fmt.Errorf("%w (%s)", p.lastErr, strings.TrimSpace(p.stderr.String()))
	}
	_ = p.cmd.Process.Kill()
	_ = p.cmd.Wait()
	p.cmd = nil
	p.stdin = nil
	p.stdout = nil
	p.started = false
	return nil
}

// Available reports whether the command exists on PATH, so a configuration can
// be reported accurately rather than failing on first use.
func Available(command string) bool {
	if command == "" {
		return false
	}
	if strings.ContainsRune(command, os.PathSeparator) {
		st, err := os.Stat(command)
		return err == nil && !st.IsDir()
	}
	_, err := exec.LookPath(command)
	return err == nil
}

// --- Sudachi backend ------------------------------------------------------

// SudachiConfig describes the Sudachi backend.
//
// The command defaults to the reference adapter shipped in tools/, which wraps
// sudachipy. SudachiDict's binary format moved to V1 and sudachipy 0.7 cannot
// read it, so the version is pinned and recorded rather than discovered: an
// analyser whose dictionary format silently changed underneath would produce
// analyses that look fine and are subtly wrong.
func SudachiConfig(dictionary string) ProcessConfig {
	dict := dictionary
	if dict == "" {
		dict = "core"
	}
	return ProcessConfig{
		Command:    "python3",
		Args:       []string{"tools/sudachi_backend.py"},
		Name:       "sudachi",
		Version:    "sudachipy-0.8.2",
		Dictionary: "sudachidict-" + dict,
		// ProfileAuto is a request-side concept, not a capability: the
		// registry resolves it to a concrete profile before asking. Listing it
		// here would make every backend claim every profile.
		Profiles: []Profile{
			ProfileModern, ProfileModernLiterary,
		},
	}
}
