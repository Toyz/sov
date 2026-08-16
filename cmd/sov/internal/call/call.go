// Package call implements `sov call` — quick interactive RPC.
// Replaces hand-rolled curl + jq for one-shot method tests.
//
//	sov call Auth.login --from http://localhost:8080 --args '{"handle":"alice","password":"pw"}'
//	sov call User.get --from $URL --args '["u_alice"]' --positional
//	sov call User.getMe --from $URL --token tok_abc
package call

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Toyz/sov/cmd/sov/internal/catalog"
)

// Run executes `sov call` with argv. Returns exit code.
//
// Args layout: first non-flag positional arg is the dotted method
// (Service.method). Remaining flags configure transport + args.
func Run(argv []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sov call", flag.ContinueOnError)
	fs.SetOutput(stderr)
	from := fs.String("from", "", "gateway base URL")
	token := fs.String("token", "", "Bearer token added to the Authorization header")
	args := fs.String("args", "", "JSON args body: '{...}' for named, '[...]' for positional. Default: empty named '{}'.")
	positional := fs.Bool("positional", false, "wrap raw args value as positional [...] when --args isn't already a JSON array")
	timeout := fs.Duration("timeout", 30*time.Second, "request timeout")
	var headers catalog.StringSliceFlag
	fs.Var(&headers, "header", "extra header K=V; repeatable")

	// Extract the dotted Service.method positional from anywhere in
	// argv so users can write it before OR after the flags. Stdlib
	// flag.Parse stops at the first non-flag; this preprocess lifts
	// the positional so flags after it still parse.
	dotted, rest := extractDotted(argv)
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if dotted == "" && fs.NArg() > 0 {
		dotted = fs.Arg(0)
	}
	if dotted == "" {
		fmt.Fprintln(stderr, "sov call: missing Service.method")
		fs.Usage()
		return 2
	}
	parts := strings.SplitN(dotted, ".", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		fmt.Fprintf(stderr, "sov call: %q must be in the form Service.method\n", dotted)
		return 2
	}
	if *from == "" {
		fmt.Fprintln(stderr, "sov call: --from <url> is required")
		fs.Usage()
		return 2
	}

	body, err := buildBody(*args, *positional)
	if err != nil {
		fmt.Fprintf(stderr, "sov call: %v\n", err)
		return 2
	}

	url := strings.TrimRight(*from, "/") + "/rpc/" + parts[0] + "/" + parts[1]
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(stderr, "sov call: build request: %v\n", err)
		return 1
	}
	req.Header.Set("Content-Type", "application/json")
	if *token != "" {
		req.Header.Set("Authorization", "Bearer "+*token)
	}
	for _, h := range headers {
		k, v, ok := strings.Cut(h, "=")
		if !ok {
			fmt.Fprintf(stderr, "sov call: invalid --header %q (want K=V)\n", h)
			return 2
		}
		req.Header.Set(k, v)
	}

	cli := &http.Client{Timeout: *timeout}
	resp, err := cli.Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "sov call: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintf(stderr, "sov call: read body: %v\n", err)
		return 1
	}

	pretty, isErr := prettyEnvelope(raw)
	fmt.Fprintf(stderr, "HTTP %d  %s\n", resp.StatusCode, url)
	if rid := resp.Header.Get("X-Sov-Request-Id"); rid != "" {
		fmt.Fprintf(stderr, "X-Sov-Request-Id: %s\n", rid)
	}
	fmt.Fprintln(stdout, pretty)
	if isErr || resp.StatusCode >= 400 {
		return 1
	}
	return 0
}

func buildBody(args string, positional bool) ([]byte, error) {
	args = strings.TrimSpace(args)
	if args == "" {
		// Default to empty named-object so dispatch accepts it.
		return []byte(`{"args":{}}`), nil
	}
	// Wrap raw value if user didn't already write the envelope.
	if json.Valid([]byte(args)) {
		// If args is already a top-level object containing "args" key,
		// assume the user wrote the envelope themselves.
		var probe map[string]json.RawMessage
		if json.Unmarshal([]byte(args), &probe) == nil {
			if _, ok := probe["args"]; ok {
				return []byte(args), nil
			}
		}
		// Otherwise wrap.
		if positional {
			// Force positional shape: wrap in [...] if not already.
			trimmed := strings.TrimSpace(args)
			if !strings.HasPrefix(trimmed, "[") {
				args = "[" + trimmed + "]"
			}
		}
		return []byte(`{"args":` + args + `}`), nil
	}
	return nil, fmt.Errorf("--args is not valid JSON: %s", args)
}

// prettyEnvelope formats the wire envelope so success vs error is
// visually obvious. Returns (formatted, isErrorEnvelope).
func prettyEnvelope(raw []byte) (string, bool) {
	var env struct {
		Data  json.RawMessage `json:"data,omitempty"`
		Error *struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error,omitempty"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return string(raw), false
	}
	if env.Error != nil {
		buf := &bytes.Buffer{}
		fmt.Fprintf(buf, "ERROR %s: %s", env.Error.Code, env.Error.Message)
		return buf.String(), true
	}
	if len(env.Data) > 0 {
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, env.Data, "", "  "); err == nil {
			return pretty.String(), false
		}
		return string(env.Data), false
	}
	return string(raw), false
}

// extractDotted lifts the first non-flag arg that looks like
// "Service.method" out of argv so flag.Parse sees the remaining
// flag/value pairs intact. Returns ("", argv) when no dotted arg
// found. Treats anything starting with "-" as a flag boundary —
// flag VALUES (the token after a value-taking flag) are left in
// place because Go flag pkg can't distinguish them positionally
// either; users with weird shell metas can always put the dotted
// arg LAST and avoid the heuristic entirely.
func extractDotted(argv []string) (string, []string) {
	for i, a := range argv {
		if len(a) == 0 {
			continue
		}
		if a[0] == '-' {
			continue
		}
		if !strings.Contains(a, ".") {
			continue
		}
		// Skip if previous arg is a value-taking flag (the dotted
		// thing is its value, not our positional). Conservative:
		// only skip if prev is a known value-flag.
		if i > 0 && isValueFlag(argv[i-1]) {
			continue
		}
		rest := append([]string(nil), argv[:i]...)
		rest = append(rest, argv[i+1:]...)
		return a, rest
	}
	return "", argv
}

func isValueFlag(prev string) bool {
	switch prev {
	case "-from", "--from", "-token", "--token", "-args", "--args",
		"-timeout", "--timeout", "-header", "--header":
		return true
	}
	return false
}
