package safetylexicon

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// dataFS holds the built-in lexicon corpus, compiled into the binary so the
// corpus is 境内-safe and never fetched over the network (OQ-8.1-2). Extending
// the corpus is a pure data-file edit under data/{zh,en}/<category>.txt — no
// registry-code change, no recompile-of-logic (BR-2.1, the "可扩展" AC).
//
//go:embed data/zh data/en
var dataFS embed.FS

// Row is a raw, pre-validation term row parsed from a data file. NewFromRegistry
// validates and resolves these into a Lexicon. Holding the raw rows separately
// (rather than a pre-resolved map) keeps the construction-time invariants — and
// the file:line provenance used in their panic messages — a real, testable
// surface (the plan-catalogue Registry/NewFromRegistry split).
type Row struct {
	Lang     Lang     // derived from the source dir (BR-1.5), NOT term content
	Category Category // derived from the data file stem
	Severity Severity // the per-term token after the tab
	Raw      string   // the raw term text, pre-normalization
	File     string   // embed path, for panic provenance
	Line     int      // 1-based line number, for panic provenance
}

// Registry is the raw corpus seed: the flat list of parsed term rows.
// NewFromRegistry resolves it into a Lexicon, enforcing the floor / enum /
// dedupe / length invariants.
type Registry struct {
	Rows []Row
}

// loadRegistryFS parses a corpus tree (data/{zh,en}/<category>.txt) from any
// fs.FS into a Registry. The embedded corpus uses it via loadEmbeddedRegistry;
// tests use it with a fstest.MapFS to exercise the unknown-category-file path
// (BLIND-ERROR-002) and the extensibility/loanword rules without touching the
// shipped corpus. The category comes from the file stem (an unknown stem
// panics); the language comes from the directory (BR-1.5). Blank lines and
// '#'-prefixed comment lines are first-class and excluded (BR-2.4); every other
// line is "<term>\t<severity>".
//
// Directory and file iteration order is sorted so the produced Registry is
// deterministic regardless of the host filesystem's readdir order.
func loadRegistryFS(fsys fs.FS) Registry {
	var rows []Row
	for _, lang := range []Lang{LangZH, LangEN} {
		dir := "data/" + string(lang)
		entries, err := fs.ReadDir(fsys, dir)
		if err != nil {
			panic(fmt.Sprintf("safetylexicon: cannot read embedded dir %s: %v", dir, err))
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasSuffix(name, ".txt") {
				continue
			}
			cat := Category(strings.TrimSuffix(name, ".txt"))
			if !categorySet[cat] {
				panic(fmt.Sprintf("safetylexicon: unknown category file %s", name))
			}

			fpath := dir + "/" + name
			content, err := fs.ReadFile(fsys, fpath)
			if err != nil {
				panic(fmt.Sprintf("safetylexicon: cannot read embedded file %s: %v", fpath, err))
			}
			rows = append(rows, parseFile(string(content), lang, cat, fpath)...)
		}
	}
	return Registry{Rows: rows}
}

// parseFile turns one data file's bytes into Rows. Each non-blank, non-comment
// line is split on the first tab into "<term>\t<severity>"; a line missing the
// tab yields an empty severity token, which NewFromRegistry rejects as an
// unknown severity (forcing every term to be explicitly severity-tagged).
func parseFile(content string, lang Lang, cat Category, fpath string) []Row {
	var rows []Row
	lineNo := 0
	for _, line := range strings.Split(content, "\n") {
		lineNo++
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		raw, sev, _ := strings.Cut(line, "\t")
		rows = append(rows, Row{
			Lang:     lang,
			Category: cat,
			Severity: Severity(strings.TrimSpace(sev)),
			Raw:      raw,
			File:     fpath,
			Line:     lineNo,
		})
	}
	return rows
}

// DefaultRegistry is the built-in corpus seed, parsed from the embedded data
// files at package init.
var DefaultRegistry = loadRegistryFS(dataFS)

// DefaultLexicon is the resolved DefaultRegistry. Built at package init (the
// models-catalogue / plan-catalogue self-binding precedent), so any corpus
// drift — a below-floor count, an unknown enum token, a duplicate term, an
// over-length term — panics at process start, before any consumer (8.2/8.3)
// can observe an under-protecting lexicon.
var DefaultLexicon = NewFromRegistry(DefaultRegistry)
