package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	_ "net/http/pprof"

	"github.com/Debian/debiman/internal/archive"
	"github.com/Debian/debiman/internal/bundled"
	"github.com/Debian/debiman/internal/commontmpl"
)

var (
	servingDir = flag.String("serving_dir",
		"/srv/man",
		"Directory in which to place the manpages which should be served")

	indexPath = flag.String("index",
		"<serving_dir>/auxserver.idx",
		"Path to an auxserver index to generate")

	syncCodenames = flag.String("sync_codenames",
		"",
		"Debian codenames to synchronize (e.g. wheezy, jessie, …)")

	syncSuites = flag.String("sync_suites",
		"testing",
		"Debian suites to synchronize (e.g. testing, unstable)")

	onlyRender = flag.String("only_render_pkgs",
		"",
		"If non-empty, a comma-separated whitelist of packages to render (for developing)")

	forceRerender = flag.Bool("force_rerender",
		false,
		"Forces all manpages to be re-rendered, even if they are up to date")

	forceReextract = flag.Bool("force_reextract",
		false,
		"Forces all manpages to be re-extracted, even if there is no newer package version")

	localMirror = flag.String("local_mirror",
		"",
		"If non-empty, a file system path to a Debian mirror, e.g. /srv/mirrors/debian on DSA-maintained machines")

	injectAssets = flag.String("inject_assets",
		"",
		"If non-empty, a file system path to a directory containing assets to overwrite")

	showVersion = flag.Bool("version",
		false,
		"Show debiman version and exit")
)

// use go build -ldflags "-X main.debimanVersion=<version>" to set the version
var debimanVersion = "HEAD"

// TODO: handle deleted packages, i.e. packages which are present on
// disk but not in pkgs

// TODO(later): add memory usage estimates to the big structures, set
// parallelism level according to available memory on the system
func logic() error {
	ar := &archive.Getter{
		ConnectionsPerMirror: 10,
		LocalMirror:          *localMirror,
	}

	// Stage 1: all Debian packages of all architectures of the
	// specified suites are discovered.
	globalView, err := buildGlobalView(ar, distributions(
		strings.Split(*syncCodenames, ","),
		strings.Split(*syncSuites, ",")))
	if err != nil {
		return err
	}

	log.Printf("gathered packages of all suites, total %d packages", len(globalView.pkgs))

	// Stage 2: man pages and auxilliary files (e.g. content fragment
	// files which are included by a number of manpages) are extracted
	// from the identified Debian packages.
	if err := parallelDownload(ar, globalView); err != nil {
		return err
	}

	log.Printf("Extracted all manpages, now rendering")

	// Stage 3: all man pages are rendered into an HTML representation
	// using mandoc(1), directory index files are rendered, contents
	// files are rendered.
	if err := renderAll(globalView); err != nil {
		return err
	}

	log.Printf("Rendered all manpages, writing index")

	// Stage 4: write the index only after all rendering is complete,
	// otherwise debiman-auxserver might serve redirects to pages
	// which cannot be served yet.
	path := strings.Replace(*indexPath, "<serving_dir>", *servingDir, -1)
	log.Printf("Writing debiman-auxserver index to %q", path)
	if err := writeIndex(path, globalView); err != nil {
		return err
	}

	if err := renderAux(*servingDir, globalView); err != nil {
		return err
	}

	return nil
}

func main() {
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lshortfile)

	if *showVersion {
		fmt.Printf("debiman %s\n", debimanVersion)
		return
	}

	if *injectAssets != "" {
		if err := bundled.Inject(*injectAssets); err != nil {
			log.Fatal(err)
		}

		commonTmpls = commontmpl.MustParseCommonTmpls()
		contentsTmpl = mustParseContentsTmpl()
		pkgindexTmpl = mustParsePkgindexTmpl()
		indexTmpl = mustParseIndexTmpl()
		faqTmpl = mustParseFaqTmpl()
		aboutTmpl = mustParseAboutTmpl()
		manpageTmpl = mustParseManpageTmpl()
		manpageerrorTmpl = mustParseManpageerrorTmpl()
	}

	// All of our .so references are relative to *servingDir. For
	// mandoc(1) to find the files, we need to change the working
	// directory now.
	if err := os.Chdir(*servingDir); err != nil {
		log.Fatal(err)
	}

	go http.ListenAndServe(":4414", nil)

	if err := logic(); err != nil {
		log.Fatal(err)
	}
}
