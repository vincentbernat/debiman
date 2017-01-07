package main

import (
	"fmt"
	"html/template"
	"io"
	"path/filepath"
	"sort"
)

var indexTmpl = template.Must(template.Must(commonTmpls.Clone()).New("index").Parse(indexContent))
var faqTmpl = template.Must(template.Must(commonTmpls.Clone()).New("faq").Parse(faqContent))

func renderAux(destDir string, gv globalView) error {
	suites := make([]string, 0, len(gv.suites))
	for suite := range gv.suites {
		suites = append(suites, suite)
	}
	sort.SliceStable(suites, func(i, j int) bool {
		orderi, oki := sortOrder[suites[i]]
		orderj, okj := sortOrder[suites[j]]
		if !oki || !okj {
			panic(fmt.Sprintf("either %q or %q is an unknown suite. known: %+v", suites[i], suites[j], sortOrder))
		}
		return orderi < orderj
	})

	if err := writeAtomically(filepath.Join(destDir, "index.html.gz"), true, func(w io.Writer) error {
		return indexTmpl.Execute(w, struct {
			Title          string
			DebimanVersion string
			Breadcrumbs    []breadcrumb
			FooterExtra    string
			Suites         []string
		}{
			Title:          "index",
			Suites:         suites,
			DebimanVersion: debimanVersion,
		})
	}); err != nil {
		return err
	}

	if err := writeAtomically(filepath.Join(destDir, "faq.html.gz"), true, func(w io.Writer) error {
		return faqTmpl.Execute(w, struct {
			Title          string
			DebimanVersion string
			Breadcrumbs    []breadcrumb
			FooterExtra    string
		}{
			Title:          "FAQ",
			DebimanVersion: debimanVersion,
		})
	}); err != nil {
		return err
	}

	for name, content := range bundled {
		if err := writeAtomically(filepath.Join(destDir, filepath.Base(name)+".gz"), true, func(w io.Writer) error {
			_, err := w.Write(content)
			return err
		}); err != nil {
			return err
		}
	}

	return nil
}
