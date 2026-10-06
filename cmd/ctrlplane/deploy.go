package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// Controllers, as the site's Controllers page: deploy (or update) one, remove one, restart one,
// and read its logs. deploy sends what the site's form sends.

type deployOpts struct {
	image, file, cpu, memory, probes, registryUser string
	env                                            []string
	webhookPort                                    int
	passwordStdin                                  bool
}

func deployCmd(cp *string) *cobra.Command {
	var o deployOpts
	c := &cobra.Command{
		Use:   "deploy [name] (--image <image> | -f deployment.yaml) [flags] [-- args...]",
		Short: "Deploy a controller on the current control plane, or update the one of that name",
		Example: `  ctrlplane deploy guestbook --image ghcr.io/you/guestbook:v1 --env LOG_LEVEL=debug -- --leader-elect
  ctrlplane deploy -f config/manager/manager.yaml       # its Deployment's image, args, env, limits, probes, webhook port
  ctrlplane deploy guestbook -f dist/install.yaml --image ghcr.io/you/guestbook:v2   # flags win over the file
  echo "$GHCR_TOKEN" | ctrlplane deploy guestbook --image ghcr.io/you/private:v1 --registry-user you --registry-password-stdin`,
		RunE: func(c *cobra.Command, args []string) error {
			names, ctrlArgs := args, []string(nil)
			if dash := c.ArgsLenAtDash(); dash >= 0 {
				names, ctrlArgs = args[:dash], args[dash:]
			}
			if len(names) > 1 {
				return fmt.Errorf("one controller name, then -- and its args (got %q)", names)
			}
			name := strings.Join(names, "")
			form, err := deployForm(name, ctrlArgs, o, os.Stdin)
			if err != nil {
				return err
			}
			return deploy(c.Context(), *cp, form)
		},
	}
	f := c.Flags()
	f.StringVar(&o.image, "image", "", "the controller's image")
	f.StringVarP(&o.file, "filename", "f", "", "a deployment.yaml (or an install.yaml with one Deployment): a file, an https:// URL, or - for stdin")
	f.StringArrayVar(&o.env, "env", nil, "an environment variable, KEY=value (repeatable)")
	f.StringVar(&o.cpu, "cpu", "", "CPU limit, e.g. 250m")
	f.StringVar(&o.memory, "memory", "", "memory limit, e.g. 128Mi")
	f.IntVar(&o.webhookPort, "webhook-port", 0, "the port it serves admission or conversion webhooks on, e.g. 9443")
	f.StringVar(&o.probes, "probes", "", "a file of Kubernetes probes: livenessProbe, readinessProbe, startupProbe")
	f.StringVar(&o.registryUser, "registry-user", "", "the username for a private image's registry")
	f.BoolVar(&o.passwordStdin, "registry-password-stdin", false, "read the registry password or token from stdin")
	return c
}

// deployForm is what the site's Deploy a controller form would send.
func deployForm(name string, args []string, o deployOpts, stdin io.Reader) (url.Values, error) {
	form := url.Values{}
	set := func(k, v string) {
		if v != "" {
			form.Set(k, v)
		}
	}
	switch {
	case o.file == "" && o.image == "":
		return nil, errors.New("give an --image, or -f with your operator's deployment.yaml")
	case o.file == "" && name == "":
		return nil, errors.New("name the controller: ctrlplane deploy <name> --image <image>")
	case o.file == "-" && o.passwordStdin:
		return nil, errors.New("stdin can't be both the file (-f -) and the registry password")
	case o.passwordStdin && o.registryUser == "":
		return nil, errors.New("--registry-password-stdin needs --registry-user")
	}
	if o.file != "" {
		form.Set("source", "yaml")
		if strings.HasPrefix(o.file, "https://") {
			form.Set("url", o.file)
		} else {
			b, err := readFile(o.file, stdin)
			if err != nil {
				return nil, err
			}
			form.Set("yaml", string(b))
		}
	}
	set("name", name)
	set("image", o.image)
	set("args", strings.Join(args, "\n"))
	set("env", strings.Join(o.env, "\n"))
	set("cpu", o.cpu)
	set("memory", o.memory)
	if o.webhookPort != 0 {
		form.Set("webhookPort", strconv.Itoa(o.webhookPort))
	}
	if o.probes != "" {
		b, err := os.ReadFile(o.probes)
		if err != nil {
			return nil, err
		}
		form.Set("probes", string(b))
	}
	if o.passwordStdin {
		b, err := io.ReadAll(stdin)
		if err != nil {
			return nil, err
		}
		form.Set("registryUser", o.registryUser)
		form.Set("registryPassword", strings.TrimSpace(string(b)))
	}
	return form, nil
}

func readFile(name string, stdin io.Reader) ([]byte, error) {
	if name == "-" {
		return io.ReadAll(stdin)
	}
	return os.ReadFile(name)
}

func deploy(ctx context.Context, cp string, form url.Values) error {
	c, name, err := controlPlane(ctx, cp)
	if err != nil {
		return err
	}
	var res struct {
		Name    string
		Updated bool
		Ignored []string
	}
	if err := c.api(ctx, "POST", "/tenants/"+url.PathEscape(name)+"/controllers", form, &res); err != nil {
		return err
	}
	verb := "Deploying"
	if res.Updated {
		verb = "Updating"
	}
	fmt.Fprintf(os.Stderr, "%s %s on %s. Watch it: ctrlplane controllers\n", verb, res.Name, name)
	if len(res.Ignored) > 0 {
		fmt.Fprintf(os.Stderr, "Not used from the Deployment: %s\n", strings.Join(res.Ignored, ", "))
	}
	return nil
}

func rmCmd(cp *string) *cobra.Command {
	return &cobra.Command{Use: "rm <controller>", Short: "Remove a controller (your resources stay)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return controllerAction(c.Context(), *cp, "DELETE", args[0], "", "Removed %s from %s.\n")
		}}
}

func restartCmd(cp *string) *cobra.Command {
	return &cobra.Command{Use: "restart <controller>", Short: "Restart a controller: its pod is replaced with a fresh one", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return controllerAction(c.Context(), *cp, "POST", args[0], "/restart", "Restarting %s on %s.\n")
		}}
}

func controllerAction(ctx context.Context, cp, method, controller, suffix, done string) error {
	c, name, err := controlPlane(ctx, cp)
	if err != nil {
		return err
	}
	if err := c.api(ctx, method, "/tenants/"+url.PathEscape(name)+"/controllers/"+url.PathEscape(controller)+suffix, nil, &struct{}{}); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, done, controller, name)
	return nil
}

func logsCmd(cp *string) *cobra.Command {
	var follow, previous bool
	var tail int
	c := &cobra.Command{Use: "logs <controller>", Short: "Print a controller's logs", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			kctx, err := kubeContext(c.Context(), *cp)
			if err != nil {
				return err
			}
			q := url.Values{"tailLines": {strconv.Itoa(tail)}, "follow": {strconv.FormatBool(follow)}, "previous": {strconv.FormatBool(previous)}}
			cmd := exec.CommandContext(c.Context(), "kubectl", "--context", kctx, "get", "--raw",
				"/platform/controllers/"+url.PathEscape(args[0])+"/logs?"+q.Encode())
			cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
			return cmd.Run()
		}}
	c.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing new lines")
	c.Flags().BoolVarP(&previous, "previous", "p", false, "the run before the last restart")
	c.Flags().IntVar(&tail, "tail", 300, "lines from the end")
	return c
}

// controlPlane is --cp's (by either name) or else the current one, with its full name.
func controlPlane(ctx context.Context, cp string) (*config, string, error) {
	if cp != "" {
		return resolve(ctx, cp)
	}
	c, err := loadConfig()
	if err != nil {
		return nil, "", err
	}
	if c.Current == "" {
		return nil, "", errors.New("no control plane selected: ctrlplane use <name>, or --cp <name>")
	}
	return c, c.Current, nil
}
