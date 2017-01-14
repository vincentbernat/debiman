package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Debian/debiman/internal/commontmpl"
	"github.com/Debian/debiman/internal/manpage"
	"golang.org/x/net/context"
	"golang.org/x/sync/errgroup"
)

type breadcrumb struct {
	Link string
	Text string
}

var commonTmpls = commontmpl.MustParseCommonTmpls()

type renderingMode int

const (
	regularFiles renderingMode = iota
	symlinks
	packageIndex
)

func walkContents(ctx context.Context, renderChan chan<- renderJob, contents map[string][]os.FileInfo, whitelist map[string]bool, mode renderingMode, gv globalView) error {
	// the invariant is: each file ending in .gz must have a corresponding .html.gz file
	// the .html.gz must have a modtime that is >= the modtime of the .gz file
	for dir, files := range contents {
		if whitelist != nil && !whitelist[filepath.Base(dir)] {
			continue
		}

		fileByName := make(map[string]os.FileInfo, len(files))
		for _, f := range files {
			fileByName[f.Name()] = f
		}

		manpageByName := make(map[string]*manpage.Meta, len(files))

		var indexModTime time.Time
		if fi, ok := fileByName["index.html.gz"]; ok {
			indexModTime = fi.ModTime()
		}
		var indexNeedsUpdate bool

		for _, f := range files {
			full := filepath.Join(dir, f.Name())
			if !strings.HasSuffix(full, ".gz") ||
				strings.HasSuffix(full, ".html.gz") {
				continue
			}

			symlink := f.Mode()&os.ModeSymlink != 0

			if mode == regularFiles && symlink ||
				mode == symlinks && !symlink {
				continue
			}

			if !indexNeedsUpdate && f.ModTime().After(indexModTime) {
				indexNeedsUpdate = true
			}

			m, err := manpage.FromServingPath(*servingDir, full)
			if err != nil {
				// If we run into this case, our code cannot correctly
				// interpret the result of ServingPath().
				log.Printf("BUG: cannot parse manpage from serving path %q: %v", full, err)
				continue
			}

			manpageByName[f.Name()] = m
			if mode == packageIndex {
				continue
			}

			n := strings.TrimSuffix(f.Name(), ".gz") + ".html.gz"
			html, ok := fileByName[n]
			if !ok || html.ModTime().Before(f.ModTime()) || *forceRerender {
				versions := gv.xref[m.Name]
				// Replace m with its corresponding entry in versions
				// so that rendermanpage() can use pointer equality to
				// efficiently skip entries.
				for _, v := range versions {
					if v.ServingPath() == m.ServingPath() {
						m = v
						break
					}
				}
				select {
				case renderChan <- renderJob{
					dest:     filepath.Join(dir, n),
					src:      full,
					meta:     m,
					versions: versions,
					xref:     gv.xref,
					modTime:  f.ModTime(),
					symlink:  symlink,
				}:
				case <-ctx.Done():
					break
				}
			}
		}

		if mode != packageIndex {
			continue
		}

		if !indexNeedsUpdate && !*forceRerender {
			continue
		}

		if len(manpageByName) == 0 {
			log.Printf("WARNING: empty directory %q, not generating package index", dir)
			continue
		}

		if err := renderPkgindex(filepath.Join(dir, "index.html.gz"), manpageByName); err != nil {
			return err
		}
	}

	return nil
}

func renderAll(gv globalView) error {
	binsBySuite := make(map[string][]string)

	suitedirs, err := ioutil.ReadDir(*servingDir)
	if err != nil {
		return err
	}
	// To minimize I/O, gather all FileInfos in one pass.
	contents := make(map[string][]os.FileInfo)
	for _, sfi := range suitedirs {
		if !sfi.IsDir() {
			continue
		}
		if !gv.suites[sfi.Name()] {
			continue
		}
		bins, err := ioutil.ReadDir(filepath.Join(*servingDir, sfi.Name()))
		if err != nil {
			return err
		}
		names := make([]string, len(bins))
		for idx, bfi := range bins {
			names[idx] = bfi.Name()
			dir := filepath.Join(*servingDir, sfi.Name(), bfi.Name())
			contents[dir], err = ioutil.ReadDir(dir)
			if err != nil {
				return err
			}
		}
		binsBySuite[sfi.Name()] = names
	}

	eg, ctx := errgroup.WithContext(context.Background())
	renderChan := make(chan renderJob)
	// TODO: flag for parallelism level
	for i := 0; i < 30; i++ {
		eg.Go(func() error {
			for r := range renderChan {
				if err := rendermanpage(r); err != nil {
					// rendermanpage writes an error page if rendering
					// failed, any returned error is severe (e.g. file
					// system full) and should lead to termination.
					return err
				}
			}
			return nil
		})
	}
	var whitelist map[string]bool
	if *onlyRender != "" {
		whitelist = make(map[string]bool)
		log.Printf("Restricting rendering to the following binary packages:")
		for _, e := range strings.Split(strings.TrimSpace(*onlyRender), ",") {
			whitelist[e] = true
			log.Printf("  %q", e)
		}
		log.Printf("(total: %d whitelist entries)", len(whitelist))
	}

	// Render all regular files first
	if err := walkContents(ctx, renderChan, contents, whitelist, regularFiles, gv); err != nil {
		return err
	}

	// then render all symlinks, re-using the rendered fragments
	if err := walkContents(ctx, renderChan, contents, whitelist, symlinks, gv); err != nil {
		return err
	}

	// and finally render the package index files which need to
	// consider both regular files and symlinks.
	if err := walkContents(ctx, renderChan, contents, whitelist, packageIndex, gv); err != nil {
		return err
	}

	close(renderChan)
	if err := eg.Wait(); err != nil {
		return err
	}

	for suite, bins := range binsBySuite {
		if err := renderContents(filepath.Join(*servingDir, fmt.Sprintf("contents-%s.html.gz", suite)), suite, bins); err != nil {
			return err
		}
	}

	return nil
}
