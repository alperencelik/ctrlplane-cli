// ctrlplane is the platform's CLI: sign in, manage your control planes, and run kubectl against
// them. Installed as kubectl-ctrlplane it is also a kubectl plugin (`kubectl ctrlplane list`).
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// version is set at release (GoReleaser: -X main.version=...).
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	err := root().ExecuteContext(ctx)
	if ee := (*exec.ExitError)(nil); errors.As(err, &ee) {
		os.Exit(ee.ExitCode()) // kubectl said why
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func root() *cobra.Command {
	var cp string
	r := &cobra.Command{Use: "ctrlplane", Short: "The CLI for ctrlplane.run: hosted Kubernetes control planes, from the terminal", SilenceUsage: true, SilenceErrors: true, Version: version}
	if strings.HasPrefix(filepath.Base(os.Args[0]), "kubectl-") {
		r.Annotations = map[string]string{cobra.CommandDisplayNameAnnotation: "kubectl ctrlplane"}
	}
	r.PersistentFlags().StringVar(&cp, "cp", "", "control plane to use instead of the current one (ctrlplane use)")
	server := "https://app.ctrlplane.run"
	loginCmd := &cobra.Command{Use: "login", Short: "Sign in with your browser", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error { return login(c.Context(), server) }}
	loginCmd.Flags().StringVar(&server, "server", server, "the platform's site (self-hosted platforms aren't supported yet)")
	wait, yes := false, false
	createCmd := &cobra.Command{Use: "create <name>", Short: "Create a control plane (named <your name>-<name>)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error { return create(c.Context(), args[0], wait) }}
	createCmd.Flags().BoolVar(&wait, "wait", false, "wait until it's ready")
	deleteCmd := &cobra.Command{Use: "delete <name>", Short: "Delete a control plane and everything in it", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error { return remove(c.Context(), args[0], yes) }}
	deleteCmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	// Thin wrappers: kubectl, on the current control plane's context, with its exact output.
	passthrough := func(use, short string, args func([]string) []string) *cobra.Command {
		return &cobra.Command{Use: use, Short: short, DisableFlagParsing: true,
			RunE: func(c *cobra.Command, a []string) error { return kubectl(c.Context(), args(a)) }}
	}
	r.AddCommand(loginCmd,
		&cobra.Command{Use: "logout", Short: "Forget your sign-in", Args: cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error { return logout() }},
		&cobra.Command{Use: "list", Aliases: []string{"ls"}, Short: "List your control planes", Args: cobra.NoArgs,
			RunE: func(c *cobra.Command, _ []string) error { return list(c.Context()) }},
		createCmd, deleteCmd,
		&cobra.Command{Use: "use <name>", Short: "Add a control plane to your kubeconfig as context ctrlplane-<name> and switch to it", Args: cobra.ExactArgs(1),
			RunE: func(c *cobra.Command, args []string) error { _, err := use(c.Context(), args[0], true); return err }},
		&cobra.Command{Use: "kubeconfig <name>", Short: "Print a control plane's kubeconfig (signs in with kubelogin)", Args: cobra.ExactArgs(1),
			RunE: func(c *cobra.Command, args []string) error { return printKubeconfig(c.Context(), args[0]) }},
		&cobra.Command{Use: "controllers", Short: "List the current control plane's controllers", Args: cobra.NoArgs,
			RunE: func(c *cobra.Command, _ []string) error { return controllers(c.Context(), cp) }},
		passthrough("apply -f <file|dir|->", "kubectl apply, on the current control plane", func(a []string) []string { return append([]string{"apply"}, a...) }),
		passthrough("get apis | get <resource...>", "kubectl get; `get apis` lists your CRDs", func(a []string) []string {
			if len(a) > 0 && a[0] == "apis" {
				a[0] = "customresourcedefinitions"
			}
			return append([]string{"get"}, a...)
		}),
		passthrough("kubectl -- <args>", "Run any kubectl command on the current control plane", func(a []string) []string {
			if len(a) > 0 && a[0] == "--" {
				a = a[1:]
			}
			return a
		}),
		&cobra.Command{Use: "token", Hidden: true, Short: "kubectl's credential plugin for ctrlplane-* contexts", Args: cobra.NoArgs,
			RunE: func(c *cobra.Command, _ []string) error { return token(c.Context()) }},
	)
	return r
}

type controlPlanes struct {
	Prefix        string `json:"prefix"` // of the names the site gives yours
	ControlPlanes []struct {
		Name, Status, Endpoint string
	} `json:"controlPlanes"`
}

func fetch(ctx context.Context) (*config, *controlPlanes, error) {
	c, err := loadConfig()
	if err != nil {
		return nil, nil, err
	}
	l := &controlPlanes{}
	return c, l, c.api(ctx, "GET", "/tenants", nil, l)
}

// resolve finds a control plane by its full name, or by the name it was created with (no prefix).
func resolve(ctx context.Context, name string) (*config, string, error) {
	c, l, err := fetch(ctx)
	if err != nil {
		return nil, "", err
	}
	for _, cp := range l.ControlPlanes {
		if cp.Name == name || cp.Name == l.Prefix+"-"+name {
			return c, cp.Name, nil
		}
	}
	return nil, "", fmt.Errorf("you have no control plane %q (ctrlplane list)", name)
}

func list(ctx context.Context) error {
	_, l, err := fetch(ctx)
	if err != nil {
		return err
	}
	if len(l.ControlPlanes) == 0 {
		fmt.Fprintln(os.Stderr, "No control planes yet: ctrlplane create <name>")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSTATUS\tENDPOINT")
	for _, cp := range l.ControlPlanes {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", cp.Name, cp.Status, cp.Endpoint)
	}
	return tw.Flush()
}

func create(ctx context.Context, name string, wait bool) error {
	c, err := loadConfig()
	if err != nil {
		return err
	}
	var made struct{ Name string }
	if err := c.api(ctx, "POST", "/tenants", url.Values{"name": {name}}, &made); err != nil {
		return err
	}
	if !wait {
		fmt.Fprintf(os.Stderr, "Creating %s: it's ready in about a minute (ctrlplane list).\n", made.Name)
		return nil
	}
	fmt.Fprintf(os.Stderr, "Creating %s...\n", made.Name)
	for {
		_, l, err := fetch(ctx)
		if err != nil {
			return err
		}
		for _, cp := range l.ControlPlanes {
			if cp.Name == made.Name && cp.Status == "Ready" {
				fmt.Fprintf(os.Stderr, "%s is ready at %s. Next: ctrlplane use %s\n", cp.Name, cp.Endpoint, name)
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

func remove(ctx context.Context, name string, yes bool) error {
	c, name, err := resolve(ctx, name)
	if err != nil {
		return err
	}
	if !yes {
		fmt.Fprintf(os.Stderr, "Delete %s, with its API server, controllers and everything stored in it? [y/N] ", name)
		ans, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(ans)); a != "y" && a != "yes" {
			return errors.New("not deleted")
		}
	}
	if err := c.api(ctx, "DELETE", "/tenants/"+url.PathEscape(name), nil, &struct{}{}); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Deleting %s.\n", name)
	if c.Current == name {
		c.Current = ""
		return c.save()
	}
	return nil
}

func printKubeconfig(ctx context.Context, name string) error {
	c, name, err := resolve(ctx, name)
	if err != nil {
		return err
	}
	var b []byte
	if err := c.api(ctx, "GET", "/tenants/"+url.PathEscape(name)+"/kubeconfig", nil, &b); err != nil {
		return err
	}
	_, err = os.Stdout.Write(b)
	return err
}

// use merges name's kubeconfig into yours (KUBECONFIG or ~/.kube/config) as context
// ctrlplane-<name>, switching to it (and making it current here) if switchTo.
func use(ctx context.Context, name string, switchTo bool) (string, error) {
	c, name, err := resolve(ctx, name)
	if err != nil {
		return "", err
	}
	var b []byte
	if err := c.api(ctx, "GET", "/tenants/"+url.PathEscape(name)+"/kubeconfig", nil, &b); err != nil {
		return "", err
	}
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	key, err := mergeKubeconfig(name, b, self, switchTo)
	if err != nil || !switchTo {
		return key, err
	}
	c.Current = name
	if err := c.save(); err != nil {
		return "", err
	}
	fmt.Fprintf(os.Stderr, "Switched to context %q.\n", key)
	return key, nil
}

// mergeKubeconfig adds the platform's kubeconfig for control plane name as context
// ctrlplane-<name> (its cluster and user named the same). The user is this CLI (`token`), not
// kubelogin: no second sign-in, and nothing else to install.
func mergeKubeconfig(name string, downloaded []byte, self string, switchTo bool) (string, error) {
	in, err := clientcmd.Load(downloaded)
	if err != nil {
		return "", err
	}
	src := in.Contexts[in.CurrentContext]
	if src == nil || in.Clusters[src.Cluster] == nil {
		return "", errors.New("the platform sent a kubeconfig without a current context")
	}
	po := clientcmd.NewDefaultPathOptions() // KUBECONFIG, else ~/.kube/config
	cfg, err := po.GetStartingConfig()
	if err != nil {
		return "", err
	}
	key := "ctrlplane-" + name
	cfg.Clusters[key] = in.Clusters[src.Cluster]
	cfg.AuthInfos[key] = &clientcmdapi.AuthInfo{Exec: &clientcmdapi.ExecConfig{APIVersion: "client.authentication.k8s.io/v1",
		Command: self, Args: []string{"token"}, InteractiveMode: clientcmdapi.NeverExecInteractiveMode}}
	cfg.Contexts[key] = &clientcmdapi.Context{Cluster: key, AuthInfo: key, Namespace: src.Namespace}
	if switchTo {
		cfg.CurrentContext = key
	}
	return key, clientcmd.ModifyConfig(po, *cfg, true)
}

// token prints the ID token as kubectl's ExecCredential.
func token(ctx context.Context) error {
	c, err := loadConfig()
	if err != nil {
		return err
	}
	tok, err := c.idToken(ctx)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"apiVersion": "client.authentication.k8s.io/v1", "kind": "ExecCredential",
		"status": map[string]string{"token": tok, "expirationTimestamp": c.Expiry.UTC().Format(time.RFC3339)}})
}

// kubeContext is the context of --cp (merged now, without switching) or else of the current
// control plane.
func kubeContext(ctx context.Context, cp string) (string, error) {
	if cp != "" {
		return use(ctx, cp, false)
	}
	c, err := loadConfig()
	if err != nil {
		return "", err
	}
	if c.Current == "" {
		return "", errors.New("no control plane selected: ctrlplane use <name>, or --cp <name>")
	}
	return "ctrlplane-" + c.Current, nil
}

// kubectl runs kubectl with args on the control plane's context. --cp is taken out of args
// (these commands leave their flags to kubectl).
func kubectl(ctx context.Context, args []string) error {
	cp := ""
	for i := 0; i < len(args); i++ {
		if v, ok := strings.CutPrefix(args[i], "--cp="); ok {
			cp, args = v, slices.Delete(args, i, i+1)
			i--
		} else if args[i] == "--cp" && i+1 < len(args) {
			cp, args = args[i+1], slices.Delete(args, i, i+2)
			i--
		}
	}
	kctx, err := kubeContext(ctx, cp)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "kubectl", append([]string{"--context", kctx}, args...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// controllers lists the control plane's controller pods, from its API server's
// /platform/controllers.
func controllers(ctx context.Context, cp string) error {
	kctx, err := kubeContext(ctx, cp)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "kubectl", "--context", kctx, "get", "--raw", "/platform/controllers")
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return err
	}
	var pods []struct {
		Controller, Pod, Phase string
		Ready                  bool
		Restarts               int32
		StartedAt              *time.Time
		LastTerminationReason  string
	}
	if err := json.Unmarshal(out, &pods); err != nil {
		return err
	}
	if len(pods) == 0 {
		fmt.Fprintln(os.Stderr, "No controllers running. Deploy one on the site.")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "NAME\tREADY\tSTATUS\tRESTARTS\tAGE\tLAST EXIT")
	for _, p := range pods {
		age := "-"
		if p.StartedAt != nil {
			age = duration.HumanDuration(time.Since(*p.StartedAt))
		}
		fmt.Fprintf(tw, "%s\t%v\t%s\t%d\t%s\t%s\n", p.Controller, p.Ready, p.Phase, p.Restarts, age, p.LastTerminationReason)
	}
	return tw.Flush()
}
