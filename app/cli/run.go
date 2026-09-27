package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

type multiFlag []string

func (m *multiFlag) String() string {
	return strings.Join(*m, ",")
}

func (m *multiFlag) Set(value string) error {
	*m = append(*m, value)
	return nil
}

const runUsage = "usage: a1s run <image> [--name NAME] [--cmd CMD] [--restart-policy POLICY] [--env KEY=VALUE]..."

// valueFlags lists the run flags that consume a separate value, so the image
// can be split out whether it comes before or after the flags.
var valueFlags = map[string]bool{"--name": true, "--cmd": true, "--env": true, "--restart-policy": true}

// splitImageArg peels the first positional argument (the image) off the
// command line, supporting both `a1s run nginx --name x` and
// `a1s run --name x nginx` despite the flag package's stop-at-first-argument
// parsing.
func splitImageArg(args []string) (string, []string) {
	for i := 0; i < len(args); i++ {
		arg := args[i]

		if arg == "--" {
			break
		}

		if !strings.HasPrefix(arg, "-") {
			rest := append(append([]string{}, args[:i]...), args[i+1:]...)
			return arg, rest
		}

		base, _, _ := strings.Cut(arg, "=")
		if valueFlags[base] && !strings.Contains(arg, "=") {
			i++ // the flag's value follows on the next argument
		}
	}

	return "", args
}

// runContainers handles `a1s run <image> [--name] [--cmd] [--env K=V]...`.
func runContainers(args []string) int {
	image, flagArgs := splitImageArg(args)

	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	name := fs.String("name", "", "container name")
	command := fs.String("cmd", "", "command override")
	restartPolicy := fs.String("restart-policy", "", "no | on-failure[:N] | always | unless-stopped")
	envList := multiFlag{}
	fs.Var(&envList, "env", "environment variable KEY=VALUE (repeatable)")

	if err := fs.Parse(flagArgs); err != nil || image == "" || fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, runUsage)
		return 2
	}

	body := map[string]any{"image": image}
	if *name != "" {
		body["name"] = *name
	}
	if *restartPolicy != "" {
		body["restart_policy"] = *restartPolicy
	}
	if *command != "" {
		body["command"] = *command
	}
	if len(envList) > 0 {
		env := map[string]string{}
		for _, kv := range envList {
			key, value, ok := strings.Cut(kv, "=")
			if !ok || key == "" {
				fmt.Fprintf(os.Stderr, "a1s: invalid --env %q, want KEY=VALUE\n", kv)
				return 2
			}
			env[key] = value
		}
		body["env"] = env
	}

	var created struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	client := NewClient()
	if err := client.do("POST", "/api/v1/containers", body, &created); err != nil {
		return client.fail(err)
	}

	fmt.Printf("created %s (id %d, %s)\n", created.Name, created.ID, created.Status)
	return 0
}
