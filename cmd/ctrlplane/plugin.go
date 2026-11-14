package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// Plugins, as the site's Plugins view: operators the platform runs for you. List them, turn one
// on or off on the current control plane.

func pluginCmd(cp *string) *cobra.Command {
	list := func(c *cobra.Command, _ []string) error { return plugins(c.Context(), *cp) }
	c := &cobra.Command{Use: "plugin", Aliases: []string{"plugins"}, Short: "List the plugins, or turn one on or off on the current control plane",
		Example: `  ctrlplane plugins                       # all plugins, and which are on
  ctrlplane plugin on crossplane-aws-s3   # its API types appear; the API server restarts for a few seconds
  ctrlplane plugin off crossplane-aws-s3  # refused while its resources exist`,
		Args: cobra.NoArgs, RunE: list}
	c.AddCommand(
		&cobra.Command{Use: "list", Aliases: []string{"ls"}, Short: "List the plugins, and which are on", Args: cobra.NoArgs, RunE: list},
		&cobra.Command{Use: "on <plugin>", Short: "Turn a plugin on: its API types appear in the control plane", Args: cobra.ExactArgs(1),
			RunE: func(c *cobra.Command, args []string) error { return setPlugin(c.Context(), *cp, args[0], true) }},
		&cobra.Command{Use: "off <plugin>", Short: "Turn a plugin off (refused while its resources exist)", Args: cobra.ExactArgs(1),
			RunE: func(c *cobra.Command, args []string) error { return setPlugin(c.Context(), *cp, args[0], false) }},
	)
	return c
}

func plugins(ctx context.Context, cp string) error {
	c, name, err := controlPlane(ctx, cp)
	if err != nil {
		return err
	}
	var res struct {
		Plugins []struct {
			Name, Description, Docs, Guide string
			On                             bool
		}
	}
	if err := c.api(ctx, "GET", "/tenants/"+url.PathEscape(name)+"/plugins", nil, &res); err != nil {
		return err
	}
	if len(res.Plugins) == 0 {
		fmt.Fprintln(os.Stderr, "This platform has no plugins.")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "NAME\tON\tDESCRIPTION")
	for _, p := range res.Plugins {
		fmt.Fprintf(tw, "%s\t%v\t%s\n", p.Name, p.On, p.Description)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Each plugin's API types and an example: %s/plugins?ns=%s\n", c.Server, url.QueryEscape(name))
	return nil
}

func setPlugin(ctx context.Context, cp, plugin string, on bool) error {
	c, name, err := controlPlane(ctx, cp)
	if err != nil {
		return err
	}
	method, state, done := "DELETE", "off", "Turned off %s in %s. Its API types go away; the API server restarts for a few seconds.\n"
	if on {
		method, state, done = "POST", "on", "Turned on %s in %s. Its API types appear in a few seconds, when the API server has restarted.\n"
	}
	res := struct{ Changed *bool }{}
	if err := c.api(ctx, method, "/tenants/"+url.PathEscape(name)+"/plugins/"+url.PathEscape(plugin), nil, &res); err != nil {
		return err
	}
	if res.Changed != nil && !*res.Changed { // nil: a site from before "changed"
		done = "%s is already " + state + " in %s.\n"
	}
	fmt.Fprintf(os.Stderr, done, plugin, name)
	return nil
}
