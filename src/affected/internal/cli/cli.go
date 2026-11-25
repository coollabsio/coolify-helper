// Package cli is the command line of coolify-affected. stdout carries exactly
// one JSON document; logs go to stderr.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/coollabsio/coolify-helper/src/affected/internal/api"
	"github.com/coollabsio/coolify-helper/src/affected/internal/evaluate"
	"github.com/coollabsio/coolify-helper/src/affected/internal/prune"
	"github.com/coollabsio/coolify-helper/src/affected/internal/resolvecmd"
	"github.com/coollabsio/coolify-helper/src/affected/internal/scan"
)

// Version is the tool version, set with -ldflags "-X …/internal/cli.Version=…".
var Version = "0.0.0-dev"

// DefaultMirrorRoot is where Coolify mounts the mirror volume.
const DefaultMirrorRoot = "/mirrors"

const maxInput = 16 << 20

const usage = `usage: coolify-affected <command>

commands:
	evaluate       decide deploy or skip for each application of a push (JSON on stdin)
	scan           find undeclared references of an application in a checkout (JSON on stdin)
	resolve        print the graph and trigger sets of a checkout or mirror commit (JSON on stdin)
	mirror prune   delete unused mirrors [--days 14] [--max-total-bytes N], --days 0 keeps every mirror by age
	version        print the version and schema version as JSON

flags of evaluate, resolve and mirror prune:
	--mirrors DIR  the mirror volume (default /mirrors, or $COOLIFY_AFFECTED_MIRRORS)
`

type errorDoc struct {
	SchemaVersion int    `json:"schema_version"`
	ToolVersion   string `json:"tool_version"`
	Error         struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Main runs a command and returns the exit code.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) (code int) {
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	fail := func(exit int, kind, msg string) int {
		var d errorDoc
		d.SchemaVersion, d.ToolVersion = api.SchemaVersion, Version
		d.Error.Code, d.Error.Message = kind, msg
		_ = writeJSON(stdout, d)
		log.Error(msg)
		return exit
	}
	defer func() {
		if r := recover(); r != nil {
			code = fail(api.ExitFatal, "fatal", fmt.Sprintf("internal error: %v", r))
		}
	}()
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return fail(api.ExitInvalid, "invalid_input", "missing command")
	}
	cmd, rest := args[0], args[1:]
	if cmd == "mirror" {
		if len(rest) == 0 || rest[0] != "prune" {
			return fail(api.ExitInvalid, "invalid_input", "unknown mirror command, expected: mirror prune")
		}
		cmd, rest = "mirror prune", rest[1:]
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	mirrors := fs.String("mirrors", envOr("COOLIFY_AFFECTED_MIRRORS", DefaultMirrorRoot), "mirror volume")
	_ = fs.Bool("json", true, "print JSON (always on)")
	days := fs.Int("days", 14, "mirror prune: remove mirrors unused for this many days")
	maxTotal := fs.Int64("max-total-bytes", 50<<30, "mirror prune: total size cap of the mirror volume")
	if err := fs.Parse(rest); err != nil {
		return fail(api.ExitInvalid, "invalid_input", err.Error())
	}
	if fs.NArg() > 0 {
		return fail(api.ExitInvalid, "invalid_input", "unexpected arguments: "+strings.Join(fs.Args(), " "))
	}

	switch cmd {
	case "version", "--version":
		return writeOr(stdout, api.VersionResponse{Version: Version, SchemaVersion: api.SchemaVersion}, fail)

	case "evaluate":
		var req api.EvaluateRequest
		if msg := decode(stdin, &req); msg != "" {
			return fail(api.ExitInvalid, "invalid_input", msg)
		}
		if err := req.Validate(); err != nil {
			return fail(api.ExitInvalid, "invalid_input", err.Error())
		}
		resp := evaluate.Run(context.Background(), &req, evaluate.Options{MirrorRoot: *mirrors, ToolVersion: Version, Log: log})
		return writeOr(stdout, resp, fail)

	case "scan":
		var req api.ScanRequest
		if msg := decode(stdin, &req); msg != "" {
			return fail(api.ExitInvalid, "invalid_input", msg)
		}
		if err := req.Validate(); err != nil {
			return fail(api.ExitInvalid, "invalid_input", err.Error())
		}
		resp := scan.Run(context.Background(), &req, scan.Options{ToolVersion: Version, Log: log})
		return writeOr(stdout, resp, fail)

	case "resolve":
		var req api.ResolveRequest
		if msg := decode(stdin, &req); msg != "" {
			return fail(api.ExitInvalid, "invalid_input", msg)
		}
		if err := req.Validate(); err != nil {
			return fail(api.ExitInvalid, "invalid_input", err.Error())
		}
		resp, err := resolvecmd.Run(context.Background(), &req, resolvecmd.Options{MirrorRoot: *mirrors, ToolVersion: Version, Log: log})
		if err != nil {
			var ve *api.ValidationError
			if errors.As(err, &ve) {
				return fail(api.ExitInvalid, "invalid_input", err.Error())
			}
			return fail(api.ExitFatal, "fatal", err.Error())
		}
		return writeOr(stdout, resp, fail)

	case "mirror prune":
		if *days < 0 || *maxTotal < 0 {
			return fail(api.ExitInvalid, "invalid_input", "--days and --max-total-bytes must not be negative")
		}
		resp := prune.Run(prune.Options{Root: *mirrors, MaxAge: time.Duration(*days) * 24 * time.Hour, MaxTotalBytes: *maxTotal})
		resp.ToolVersion = Version
		return writeOr(stdout, resp, fail)

	case "help", "-h", "--help":
		fmt.Fprint(stderr, usage)
		return fail(api.ExitInvalid, "invalid_input", "help requested")
	}
	fmt.Fprint(stderr, usage)
	return fail(api.ExitInvalid, "invalid_input", "unknown command "+cmd)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func decode(r io.Reader, v any) string {
	b, err := io.ReadAll(io.LimitReader(r, maxInput+1))
	if err != nil {
		return "read stdin: " + err.Error()
	}
	if len(b) > maxInput {
		return "the request is larger than 16 MiB"
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(v); err != nil {
		return "the request is not valid JSON: " + err.Error()
	}
	if dec.More() {
		return "the request has data after the JSON document"
	}
	return ""
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func writeOr(w io.Writer, v any, fail func(int, string, string) int) int {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fail(api.ExitFatal, "fatal", "encode the response: "+err.Error())
	}
	if _, err := w.Write(buf.Bytes()); err != nil {
		return api.ExitFatal
	}
	return api.ExitOK
}
