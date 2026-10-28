package edit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/cgs-earth/sal/pkg"
	"github.com/cgs-earth/sal/salmodule"
	"github.com/cgs-earth/sal/serve"
)

// Form answers the module ontology form that `sal init --salmodule` and
// `sal edit` open in the browser. The form assembles ontology.jsonld, asks
// the server to check it as it is edited, and asks it to write the file; the
// server also tells the form what the module root holds, which it reads from
// disk on every request so that a file written or deleted by hand is seen
// without a restart. One server serves both commands: the only difference is
// Mode, which the page shows, and that `sal init` starts from a blank draft
// where there is no ontology yet while an existing one is loaded for editing.
type Form struct {
	// Dir is the module root, where ontology.jsonld is written.
	Dir string
	// Namespace is the module's own salmodule:// namespace, which the checks
	// resolve the ontology's relative terms against.
	Namespace string
	// Title and Creator seed a new ontology node, from the git remote and
	// user.name like the prompts do.
	Title   string
	Creator string
	// Mode is the command that opened the form, "init" or "edit"; the page
	// names itself after it.
	Mode string
	// Written is called after ontology.jsonld has been written, so the
	// command can say so in its log.
	Written func()
}

// Status is what GET /api/module/status answers.
type Status struct {
	Mode string `json:"mode"`
	// Initialized reports whether ontology.jsonld exists in the module root.
	Initialized  bool   `json:"initialized"`
	OntologyPath string `json:"ontologyPath"`
	Namespace    string `json:"namespace"`
	Title        string `json:"title"`
	Creator      string `json:"creator"`
	// Document is the current text of ontology.jsonld, which the form edits
	// from, or "" when there is none.
	Document string `json:"document"`
	// Dockerfile reports whether the module has a Dockerfile yet.
	Dockerfile bool `json:"dockerfile"`
}

// Problem is one problem with the document the form assembled, on the line
// of the document the form showed, so the form can mark it there.
type Problem struct {
	Line    int    `json:"line"`
	Message string `json:"message"`
}

// documentRequest is the body of a validate or write request: the
// exact text the form would write, so that the lines problems are reported
// on are the lines the form shows. Base is, on a write, the text the form
// loaded from disk, so that a page left open while the file was edited by
// hand does not write over those edits.
type documentRequest struct {
	Document string `json:"document"`
	Base     string `json:"base"`
}

// maxRequest bounds a request body; an ontology document is a few kilobytes,
// and nothing legitimate approaches this.
const maxRequest = 4 << 20

// Handler routes the form's JSON endpoints and serves the embedded page for
// everything else.
func (f *Form) Handler() (http.Handler, error) {
	page, err := serve.ModuleFormUI()
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/module/status", f.status)
	mux.HandleFunc("POST /api/module/validate", f.validate)
	mux.HandleFunc("POST /api/module/write", f.write)
	mux.Handle("/", page)
	return mux, nil
}

func (f *Form) ontologyPath() string {
	return filepath.Join(f.Dir, salmodule.OntologyFile)
}

// document is the current text of ontology.jsonld, or "" with false when
// there is none.
func (f *Form) document() (string, bool, error) {
	content, err := os.ReadFile(f.ontologyPath())
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return string(content), true, nil
}

func (f *Form) status(w http.ResponseWriter, r *http.Request) {
	document, exists, err := f.document()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "read "+f.ontologyPath()+": "+err.Error())
		return
	}
	dockerfile, err := os.Stat(filepath.Join(f.Dir, salmodule.DockerfileName))
	writeJSON(w, Status{
		Mode:         f.Mode,
		Initialized:  exists,
		OntologyPath: f.ontologyPath(),
		Namespace:    f.Namespace,
		Title:        f.Title,
		Creator:      f.Creator,
		Document:     document,
		Dockerfile:   err == nil && !dockerfile.IsDir(),
	})
}

// readRequest reads the document a request carries, answering the request
// itself when it cannot.
func readRequest(w http.ResponseWriter, r *http.Request) (documentRequest, bool) {
	defer func() {
		if err := r.Body.Close(); err != nil {
			slog.Error("failed to close the request body", "error", err)
		}
	}()
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequest))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "read the request: "+err.Error())
		return documentRequest{}, false
	}
	var request documentRequest
	if err := json.Unmarshal(body, &request); err != nil {
		writeJSONError(w, http.StatusBadRequest, "the request body must be a JSON object with a \"document\" string: "+err.Error())
		return documentRequest{}, false
	}
	if strings.TrimSpace(request.Document) == "" {
		writeJSONError(w, http.StatusBadRequest, "the request names no document")
		return documentRequest{}, false
	}
	return request, true
}

// Problems runs every check `sal validate --salmodule` would over the
// document, reported against the lines of the document itself.
func (f *Form) Problems(document string) []Problem {
	problems := []Problem{}
	for _, err := range salmodule.CheckOntologyDocument(salmodule.OntologyFile, []byte(document), f.Namespace) {
		var problem salmodule.ModuleError
		if errors.As(err, &problem) {
			problems = append(problems, Problem{Line: problem.Line, Message: problem.Message})
			continue
		}
		problems = append(problems, Problem{Message: err.Error()})
	}
	return problems
}

func (f *Form) validate(w http.ResponseWriter, r *http.Request) {
	request, ok := readRequest(w, r)
	if !ok {
		return
	}
	writeJSON(w, map[string]any{"problems": f.Problems(request.Document)})
}

// write writes the document as ontology.jsonld, creating it or replacing the
// one the form loaded, with the Dockerfile, .dockerignore, and editor schema
// `sal init --salmodule` scaffolds next to it where they are missing, once
// the document passes every check. A file that no longer holds the text the
// form loaded was edited by hand since, and is refused so that the page's
// copy does not write over that.
func (f *Form) write(w http.ResponseWriter, r *http.Request) {
	request, ok := readRequest(w, r)
	if !ok {
		return
	}
	current, exists, err := f.document()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "read "+f.ontologyPath()+": "+err.Error())
		return
	}
	if exists && current != request.Base {
		writeJSONError(w, http.StatusConflict, salmodule.OntologyFile+" changed on disk since this page loaded it; reload the page to edit the current file")
		return
	}
	if problems := f.Problems(request.Document); len(problems) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		if err := json.NewEncoder(w).Encode(map[string]any{"error": "the ontology has problems that must be fixed before it is written", "problems": problems}); err != nil {
			slog.Error("failed to write JSON response", "error", err)
		}
		return
	}
	if err := salmodule.WriteModuleFiles(f.Dir, request.Document, true); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := salmodule.WriteEditorSchema(f.Dir); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if f.Written != nil {
		f.Written()
	}
	writeJSON(w, map[string]string{"ontologyPath": f.ontologyPath()})
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("failed to write JSON response", "error", err)
	}
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]string{"error": message}); err != nil {
		slog.Error("failed to write JSON error response", "error", err)
	}
}

// NewForm describes the module in dir for the form: its namespace from the
// git remote, and the title and creator a new ontology node starts with.
func NewForm(dir string, mode string) (*Form, error) {
	remote, err := pkg.DefaultGitRemote()
	if err != nil {
		return nil, err
	}
	namespace, err := salmodule.ModuleNamespaceForRemote(remote)
	if err != nil {
		return nil, err
	}
	title, err := pkg.GitProjectName()
	if err != nil {
		return nil, err
	}
	creator, err := pkg.GitUserName()
	if err != nil {
		return nil, err
	}
	return &Form{Dir: dir, Namespace: namespace, Title: title, Creator: creator, Mode: mode}, nil
}

// Serve serves the form on a localhost port until the process is
// interrupted, opening it in the browser unless openBrowser is false or no
// browser is likely to open. port 0 picks an ephemeral port, so that the
// form never collides with a `sal serve` on 8080 or with a second module
// being edited at the same time; a fixed one is for forwarding over SSH.
// The server keeps running once the file has been written, since the form
// edits as well as creates, until the user stops it.
func (f *Form) Serve(port int, openBrowser bool) error {
	handler, err := f.Handler()
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("listen for the module ontology form: %w", err)
	}
	url := "http://" + listener.Addr().String() + "/"
	slog.Info("Edit the SAL module ontology at " + url + " (press Ctrl+C to stop)")
	if reason := pkg.HeadlessReason(); reason != "" && !openBrowser {
		slog.Info("not opening the browser since " + reason + "; open the address yourself")
	} else if err := pkg.OpenBrowser(url); err != nil {
		slog.Warn("could not open the browser; open the address yourself", "error", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	httpServer := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	errs := make(chan error, 1)
	go func() { errs <- httpServer.Serve(listener) }()
	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if _, exists, _ := f.document(); !exists {
		slog.Warn("the form was closed before " + salmodule.OntologyFile + " was written; run `sal init --salmodule` again to create it")
	}
	return nil
}
