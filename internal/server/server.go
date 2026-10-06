// Package server exposes the pipeline over HTTP and serves the WebUI.
//
// The API returns the whole circuit, not just a translation. A caller that
// only reads result.selected can, but every byte needed to audit *why* that
// string was chosen is in the same response.
package server

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/nico/jev-trans/internal/jev"
	"github.com/nico/jev-trans/internal/lang"
	"github.com/nico/jev-trans/internal/lexicon"
	"github.com/nico/jev-trans/internal/ontology"
	"github.com/nico/jev-trans/internal/pipeline"
	"github.com/nico/jev-trans/internal/plan"
)

// webFS holds the WebUI. The UI sources live under internal/server/web so
// they can be embedded; the repository keeps them there rather than duplicating
// them at the root.
//
//go:embed all:web
var webFS embed.FS

// Version is stamped into /api/health.
const Version = "0.1.0"

// Config configures the HTTP server.
type Config struct {
	Engine   *pipeline.Engine
	Jev      *jev.Client
	Addr     string
	ReadOnly bool
}

// Server is the HTTP handler set.
type Server struct {
	cfg Config
	mux *http.ServeMux
}

// New builds the server and its routes.
func New(cfg Config) *Server {
	s := &Server{cfg: cfg, mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) routes() {
	sub, err := fs.Sub(webFS, "web")
	if err == nil {
		s.mux.Handle("/", http.FileServer(http.FS(sub)))
	}
	s.mux.HandleFunc("/api/translate", s.handleTranslate)
	s.mux.HandleFunc("/api/disambiguate", s.handleDisambiguate)
	s.mux.HandleFunc("/api/document/reset", s.handleReset)
	s.mux.HandleFunc("/api/ontology", s.handleOntology)
	s.mux.HandleFunc("/api/lexicon", s.handleLexicon)
	s.mux.HandleFunc("/api/health", s.handleHealth)
}

// Handler returns the root handler.
func (s *Server) Handler() http.Handler { return withCORS(s.mux) }

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type errorBody struct {
	Error string `json:"error"`
	Code  int    `json:"code,omitempty"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, errorBody{Error: err.Error(), Code: code})
}

type translateRequest struct {
	Text       string           `json:"text"`
	SourceLang string           `json:"sourceLang"`
	TargetLang string           `json:"targetLang"`
	Style      *styleRequest    `json:"style,omitempty"`
	DocumentID string           `json:"documentId"`
	Mode       string           `json:"mode"`
	Answer     *pipeline.Answer `json:"answer,omitempty"`
}

type styleRequest struct {
	Politeness          *float64 `json:"politeness,omitempty"`
	Register            string   `json:"register,omitempty"`
	PronounExplicitness *float64 `json:"pronounExplicitness,omitempty"`
	Literaryness        *float64 `json:"literaryness,omitempty"`
}

func (sr *styleRequest) profile() plan.StyleProfile {
	p := plan.StyleProfile{Register: "neutral", Politeness: 0.5, PronounExplicitness: 0.3}
	if sr == nil {
		return p
	}
	if sr.Politeness != nil {
		p.Politeness = clamp01(*sr.Politeness)
	}
	if sr.PronounExplicitness != nil {
		p.PronounExplicitness = clamp01(*sr.PronounExplicitness)
	}
	if sr.Literaryness != nil {
		p.Literaryness = clamp01(*sr.Literaryness)
	}
	if sr.Register != "" {
		p.Register = sr.Register
	}
	return p
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// decode reads a JSON body. Unknown fields are tolerated on purpose: the WebUI
// is allowed to send a richer style object than the server understands, and
// dropping those fields is friendlier than rejecting the request.
func (s *Server) decode(r *http.Request, dst any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	return nil
}

func (s *Server) handleTranslate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, errors.New("use POST"))
		return
	}
	var req translateRequest
	if err := s.decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	src, ok := lang.Parse(req.SourceLang)
	if !ok {
		writeErr(w, http.StatusBadRequest, errors.New("sourceLang must be ja or en"))
		return
	}
	if req.TargetLang == "" {
		req.TargetLang = string(src.Other())
	}
	tgt, ok := lang.Parse(req.TargetLang)
	if !ok {
		writeErr(w, http.StatusBadRequest, errors.New("targetLang must be ja or en"))
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		writeErr(w, http.StatusBadRequest, pipeline.ErrEmptyInput)
		return
	}
	if req.DocumentID == "" {
		req.DocumentID = "default"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	resp, err := s.cfg.Engine.Translate(ctx, pipeline.Request{
		Text:       req.Text,
		SourceLang: src,
		TargetLang: tgt,
		Style:      req.Style.profile(),
		DocumentID: req.DocumentID,
		Mode:       req.Mode,
		Answer:     req.Answer,
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleDisambiguate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, errors.New("use POST"))
		return
	}
	var req translateRequest
	if err := s.decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Answer == nil || req.Answer.QuestionID == "" {
		writeErr(w, http.StatusBadRequest, errors.New("answer with questionId and option is required"))
		return
	}
	src, _ := lang.Parse(req.SourceLang)
	if !src.Valid() {
		src = lang.DetectLang(req.Text)
	}
	if req.TargetLang == "" {
		req.TargetLang = string(src.Other())
	}
	tgt, _ := lang.Parse(req.TargetLang)
	if !tgt.Valid() {
		tgt = src.Other()
	}
	if req.DocumentID == "" {
		req.DocumentID = "default"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	resp, err := s.cfg.Engine.Translate(ctx, pipeline.Request{
		Text:       req.Text,
		SourceLang: src,
		TargetLang: tgt,
		Style:      req.Style.profile(),
		DocumentID: req.DocumentID,
		Mode:       firstNonEmpty(req.Mode, "interactive"),
		Answer:     req.Answer,
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, errors.New("use POST"))
		return
	}
	var req struct {
		DocumentID string `json:"documentId"`
	}
	if err := s.decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.DocumentID == "" {
		req.DocumentID = "default"
	}
	s.cfg.Engine.ResetStore(req.DocumentID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "documentId": req.DocumentID})
}

func (s *Server) handleOntology(w http.ResponseWriter, r *http.Request) {
	reg := ontology.Default()
	senses := reg.Senses()
	ids := make([]string, 0, len(senses))
	byID := make(map[string]ontology.Sense, len(senses))
	for _, sn := range senses {
		ids = append(ids, sn.ID)
		byID[sn.ID] = *sn
	}
	sort.Strings(ids)
	writeJSON(w, http.StatusOK, map[string]any{
		"stats":   reg.Stats(),
		"ids":     ids,
		"senses":  byID,
		"roots":   reg.Roots(),
		"version": Version,
	})
}

func (s *Server) handleLexicon(w http.ResponseWriter, r *http.Request) {
	lx := lexicon.Default()
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, http.StatusOK, map[string]any{"stats": lx.Stats(), "entries": nil})
		return
	}
	type entry struct {
		Query  string             `json:"query"`
		Lang   string             `json:"lang"`
		Senses []lexicon.SenseHit `json:"senses,omitempty"`
		Idiom  *lexicon.Idiom     `json:"idiom,omitempty"`
		Term   *lexicon.Term      `json:"term,omitempty"`
	}
	var out []entry
	if jp := lx.SensesJP(q); len(jp) > 0 {
		out = append(out, entry{Query: q, Lang: "ja", Senses: jp})
	}
	if en := lx.SensesEN(q); len(en) > 0 {
		out = append(out, entry{Query: q, Lang: "en", Senses: en})
	}
	if id, ok := lx.Idiom(q); ok {
		out = append(out, entry{Query: q, Idiom: id})
	}
	if t, ok := lx.Term(q); ok {
		out = append(out, entry{Query: q, Term: t})
	}
	writeJSON(w, http.StatusOK, map[string]any{"stats": lx.Stats(), "entries": out})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	configured := s.cfg.Jev != nil && s.cfg.Jev.Enabled()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"version": Version,
		"jev": map[string]any{
			"configured": configured,
			"model":      "jev-1.13-free",
			"endpoint":   "https://opencode.ai/zen/v1/systemone",
		},
		"sourceLang": string(lang.JA),
		"targetLang": string(lang.EN),
		// Which predicate knowledge answered. Without this an unresolved
		// predicate looks like a bug in the system rather than a word nothing
		// configured here knows, and the two lead to completely different next
		// steps for whoever is reading the trace.
		"predicateProviders": s.cfg.Engine.PredicateProviders(),
	})
}

// Serve starts the HTTP server. It blocks until the context is cancelled.
func (s *Server) Serve(ctx context.Context) error {
	addr := s.cfg.Addr
	if addr == "" {
		addr = envOr("JEV_ADDR", "127.0.0.1:8080")
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
			return
		}
		errc <- nil
	}()
	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutCtx)
	case err := <-errc:
		return err
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
