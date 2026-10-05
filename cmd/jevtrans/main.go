// Command jevtrans runs the translation system: as a server with the WebUI,
// or as a one-shot CLI for scripting and inspection.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nico/jev-trans/internal/jev"
	"github.com/nico/jev-trans/internal/lang"
	"github.com/nico/jev-trans/internal/lex"
	"github.com/nico/jev-trans/internal/lexicon"
	"github.com/nico/jev-trans/internal/ontology"
	"github.com/nico/jev-trans/internal/pipeline"
	"github.com/nico/jev-trans/internal/plan"
	"github.com/nico/jev-trans/internal/server"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = cmdServe(os.Args[2:])
	case "translate":
		err = cmdTranslate(os.Args[2:], false)
	case "trace":
		err = cmdTranslate(os.Args[2:], true)
	case "ontology":
		err = cmdOntology(os.Args[2:])
	case "lex":
		err = cmdLex(os.Args[2:])
	case "version":
		fmt.Printf("jevtrans %s\n", server.Version)
	case "help", "-h", "--help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `jev-trans — bidirectional constraint-based JA<->EN translation

  jevtrans serve    [--addr 127.0.0.1:8080]   run the HTTP API and WebUI
  jevtrans translate --text "..." [--src ja] [--tgt en] [--style formal]
                     [--doc id] [--mode auto|interactive|strict] [--json]
  jevtrans trace     --text "..." [...]        same, but dump the full circuit
  jevtrans ontology  [--id SENSE] [--search word]
  jevtrans lex       --query 渡す
  jevtrans version

The decision oracle uses OpenCode Zen model jev-1.13-free. Set OPENCODE_API_KEY
to enable it; without a key the system runs on deterministic analysis priors and
reports every decision as source=prior.
`)
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "", "listen address (default $JEV_ADDR or 127.0.0.1:8080)")
	model := fs.String("model", "jev-1.13-free", "oracle model id")
	offline := fs.Bool("offline", false, "never call the oracle, use priors only")
	morph := fs.String("morph", "auto",
		"morphological backend: builtin, or a command speaking the analysis protocol")
	dict := fs.String("sudachi-dict", "core", "SudachiDict build to use when the sudachi backend is available")
	profile := fs.String("morph-profile", "auto",
		"lexicon profile: auto | modern | modern-literary | old-kana-colloquial")
	if err := fs.Parse(args); err != nil {
		return err
	}

	client := jev.New(jev.Options{Model: *model, Offline: *offline})
	registry := lex.NewRegistry()
	external := false
	if *morph != "builtin" && *morph != "auto" {
		registry.Register(lex.NewProcessAnalyzer(lex.ProcessConfig{
			Command: *morph, Name: *morph, Profiles: nil,
		}))
		external = true
	} else if *morph == "auto" {
		// Auto registers Sudachi when it is actually usable and silently keeps
		// the builtin otherwise. A configured-but-missing analyser is reported
		// rather than treated as absent, because the two mean different things.
		if lex.Available("python3") && sudachiUsable() {
			registry.Register(lex.NewProcessAnalyzer(lex.SudachiConfig(*dict)))
			external = true
		}
	}
	engine := pipeline.NewEngine(pipeline.EngineConfig{
		Jev: client, Morph: registry, ExternalMorph: external,
		MorphProfile: lex.Profile(*profile),
	})
	srv := server.New(server.Config{Engine: engine, Jev: client, Addr: *addr})

	if !client.Enabled() {
		fmt.Fprintln(os.Stderr, "warning: OPENCODE_API_KEY is not set; decisions will run on analysis priors")
	}
	bind := *addr
	if bind == "" {
		bind = os.Getenv("JEV_ADDR")
	}
	if bind == "" {
		bind = "127.0.0.1:8080"
	}
	fmt.Fprintf(os.Stderr, "jev-trans %s listening on http://%s (oracle: %s)\n",
		server.Version, bind, oracleState(client))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return srv.Serve(ctx)
}

func oracleState(c *jev.Client) string {
	if c.Enabled() {
		return "live"
	}
	return "offline, priors only"
}

func cmdTranslate(args []string, dumpTrace bool) error {
	name := "translate"
	if dumpTrace {
		name = "trace"
	}
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	text := fs.String("text", "", "source text (required)")
	src := fs.String("src", "", "source language: ja or en (default: detect)")
	tgt := fs.String("tgt", "", "target language: ja or en (default: the other one)")
	style := fs.String("style", "neutral", "neutral | formal | casual | technical | literary")
	politeness := fs.Float64("politeness", -1, "0 plain .. 1 polite (overrides --style)")
	pronouns := fs.Float64("pronouns", -1, "0 suppress pronouns .. 1 always overt")
	doc := fs.String("doc", "cli", "document id; reuse it to keep discourse state")
	mode := fs.String("mode", "auto", "auto | interactive | strict")
	asJSON := fs.Bool("json", false, "emit the full JSON response")
	model := fs.String("model", "jev-1.13-free", "oracle model id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*text) == "" && len(fs.Args()) > 0 {
		*text = strings.Join(fs.Args(), " ")
	}
	if strings.TrimSpace(*text) == "" {
		return fmt.Errorf("--text is required")
	}

	sLang := resolveLang(*src, *text)
	tLang := resolveLang(*tgt, string(sLang.Other()))
	if sLang == tLang {
		tLang = sLang.Other()
	}

	client := jev.New(jev.Options{Model: *model})
	engine := pipeline.NewEngine(pipeline.EngineConfig{Jev: client})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	resp, err := engine.Translate(ctx, pipeline.Request{
		Text:       *text,
		SourceLang: sLang,
		TargetLang: tLang,
		Style:      styleProfile(*style, *politeness, *pronouns),
		DocumentID: *doc,
		Mode:       *mode,
	})
	if err != nil {
		return err
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		return enc.Encode(resp)
	}
	printHuman(resp)
	if dumpTrace {
		b, err := json.MarshalIndent(resp.Trace, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout)
		os.Stdout.Write(b)
	}
	return nil
}

func resolveLang(s, text string) lang.Lang {
	if s != "" {
		if l, ok := lang.Parse(s); ok {
			return l
		}
	}
	return lang.DetectLang(text)
}

func styleProfile(register string, politeness, pronouns float64) plan.StyleProfile {
	p := plan.StyleProfile{Register: register, Politeness: 0.5, PronounExplicitness: 0.3}
	switch register {
	case "formal":
		p.Politeness = 0.8
	case "casual":
		p.Politeness = 0.1
	case "literary":
		p.Literaryness = 0.7
		p.Politeness = 0.3
	case "technical":
		p.Politeness = 0.4
	}
	if politeness >= 0 {
		p.Politeness = politeness
	}
	if pronouns >= 0 {
		p.PronounExplicitness = pronouns
	}
	return p
}

func printHuman(r *pipeline.Response) {
	rule := strings.Repeat("-", 74)
	fmt.Printf("  %s\n  %s\n", r.Source.Text, rule)
	if r.Result.Selected != nil {
		fmt.Printf("  %s\n", r.Result.Selected.Text)
	} else {
		fmt.Printf("  (no candidate passed verification)\n")
	}
	fmt.Printf("  %s\n\n", rule)
	fmt.Printf("  direction     %s -> %s\n", r.Source.Lang, r.Target.Lang)
	fmt.Printf("  status        %s\n", r.Result.Status)
	if sel := r.Result.Selected; sel != nil {
		fmt.Printf("  confidence    %.2f\n", sel.Confidence.Overall)
		fmt.Printf("  loss          propositional %.2f  referential %.2f  temporal %.2f\n",
			sel.Loss.Propositional, sel.Loss.Referential, sel.Loss.Temporal)
		fmt.Printf("                pragmatic %.2f  stylistic %.2f  implicature %.2f\n",
			sel.Loss.Pragmatic, sel.Loss.Stylistic, sel.Loss.Implicature)
		if len(sel.Constructions) > 0 {
			fmt.Printf("  construction  %s\n", strings.Join(sel.Constructions, ", "))
		}
		for _, n := range sel.Notes {
			fmt.Printf("  note          %s\n", n)
		}
	}
	if n := len(r.Result.Candidates); n > 1 {
		fmt.Printf("\n  candidates (%d)\n", n)
		for _, c := range r.Result.Candidates {
			fmt.Printf("    [%s] %s\n", c.Status, c.Text)
		}
	}
	if len(r.Result.Interpretations) > 1 {
		fmt.Printf("\n  source readings kept open\n")
		for _, in := range r.Result.Interpretations {
			fmt.Printf("    %.2f  %s (%s)\n", in.Weight, in.Label, in.Origin)
		}
	}
	if len(r.Result.Questions) > 0 {
		fmt.Printf("\n  questions\n")
		for _, q := range r.Result.Questions {
			fmt.Printf("    %s\n", q.Prompt)
			for _, o := range q.Options {
				fmt.Printf("      %-10s %.2f  %s\n", o.Key, o.Probability, o.Label)
			}
		}
	}
	if len(r.Artifacts.Decisions) > 0 {
		fmt.Printf("\n  decisions (%d)\n", len(r.Artifacts.Decisions))
		for _, d := range r.Artifacts.Decisions {
			if d.Skipped {
				fmt.Printf("    %-3s %-6s skipped: %s\n", d.Stage, d.Source, truncate(d.SkipReason, 44))
				continue
			}
			fmt.Printf("    %-3s %-6s %-46s -> %s (%.2f)\n",
				d.Stage, d.Source, truncate(d.Question, 46), truncate(d.Winner, 24), d.Confidence)
		}
	}
	if len(r.Warnings) > 0 {
		fmt.Printf("\n  warnings\n")
		for _, w := range r.Warnings {
			fmt.Printf("    - %s\n", w)
		}
	}
	fmt.Printf("\n  circuit: %d events in %.0fms, %d oracle questions (%d cached)\n",
		r.Summary.Events, r.Summary.TotalMS, r.Summary.JevCost.Questions, r.Summary.JevCost.Cached)
}

func truncate(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	if n <= 1 {
		return string(rs[:n])
	}
	return string(rs[:n-1]) + "…"
}

func cmdOntology(args []string) error {
	fs := flag.NewFlagSet("ontology", flag.ExitOnError)
	id := fs.String("id", "", "print one sense by id")
	search := fs.String("search", "", "search names and glosses")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	reg := ontology.Default()
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"stats": reg.Stats(), "senses": reg.Senses()})
	}
	if *id != "" {
		s, ok := reg.Sense(*id)
		if !ok {
			return fmt.Errorf("no sense %q", *id)
		}
		printSense(s)
		return nil
	}
	if *search != "" {
		for _, s := range reg.Find(*search) {
			printSense(s)
		}
		return nil
	}
	st := reg.Stats()
	fmt.Printf("ontology: %d senses, %d families, %d roots\n", st.Senses, st.Families, st.Roots)
	for _, s := range reg.Senses() {
		fmt.Printf("  %-22s %s\n", s.ID, s.Name)
	}
	return nil
}

func printSense(s *ontology.Sense) {
	fmt.Printf("%s  %s\n  %s\n", s.ID, s.Name, s.Gloss)
	if len(s.Args) > 0 {
		var args []string
		for _, a := range s.Args {
			mark := " "
			if a.Required {
				mark = "*"
			}
			args = append(args, mark+a.Role)
		}
		fmt.Printf("  args: %s\n", strings.Join(args, " "))
	}
	if len(s.Features) > 0 {
		var fts []string
		for k, v := range s.Features {
			fts = append(fts, k+"="+v)
		}
		fmt.Printf("  features: %s\n", strings.Join(fts, " "))
	}
	if len(s.Children) > 0 {
		fmt.Printf("  children: %s\n", strings.Join(s.Children, " "))
	}
}

func cmdLex(args []string) error {
	fs := flag.NewFlagSet("lex", flag.ExitOnError)
	q := fs.String("query", "", "surface form to look up")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *q == "" {
		return fmt.Errorf("--query is required")
	}
	lx := lexicon.Default()
	fmt.Printf("query: %s\n", *q)
	if hits := lx.SensesJP(*q); len(hits) > 0 {
		fmt.Println("  japanese:")
		for _, h := range hits {
			fmt.Printf("    %-22s %.2f  %s\n", h.SenseID, h.Weight, h.Note)
		}
	}
	if hits := lx.SensesEN(*q); len(hits) > 0 {
		fmt.Println("  english:")
		for _, h := range hits {
			fmt.Printf("    %-22s %.2f  %s\n", h.SenseID, h.Weight, h.Note)
		}
	}
	if id, ok := lx.Idiom(*q); ok {
		fmt.Printf("  idiom %s: %s (literal: %s)\n", id.ID, id.SenseID, id.Literal)
	}
	if t, ok := lx.Term(*q); ok {
		fmt.Printf("  term %s -> %s (forbidden: %s)\n", t.Concept, t.Preferred, strings.Join(t.Forbidden, ", "))
	}
	return nil
}

// sudachiUsable reports whether the Sudachi backend can actually answer here.
// It runs the reference adapter's self-test once at startup, because a backend
// that imports but cannot build its dictionary would otherwise look configured
// and fail on every sentence.
func sudachiUsable() bool {
	cmd := exec.Command("python3", "tools/sudachi_backend.py", "--selftest", "今日はいい天気ですね。")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "'")
}
