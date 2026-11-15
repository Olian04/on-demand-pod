// Command kubectl-ondemand is a thin convenience client for OnDemandPods. Every
// subcommand maps to a plain kubectl operation; see internal/plugin.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Olian04/on-demand-pod/api/v1alpha1"
	"github.com/Olian04/on-demand-pod/internal/plugin"
)

const (
	pollInterval   = 500 * time.Millisecond
	observeTimeout = 10 * time.Second
)

// env is what a command needs from the cluster, resolved lazily so --help and
// flag errors never touch a kubeconfig.
type env struct {
	flags *genericclioptions.ConfigFlags
}

func (e *env) connect() (client.Client, *rest.Config, string, error) {
	cfg, err := e.flags.ToRESTConfig()
	if err != nil {
		return nil, nil, "", fmt.Errorf("load kubeconfig: %w", err)
	}
	ns, _, err := e.flags.ToRawKubeConfigLoader().Namespace()
	if err != nil {
		return nil, nil, "", fmt.Errorf("resolve namespace: %w", err)
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, nil, "", err
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return nil, nil, "", err
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, nil, "", fmt.Errorf("create client: %w", err)
	}
	return c, cfg, ns, nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := newRootCmd().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	e := &env{flags: genericclioptions.NewConfigFlags(true)}
	root := &cobra.Command{
		Use:           "kubectl-ondemand",
		Short:         "Run pods on a lease",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	e.flags.AddFlags(root.PersistentFlags())
	root.AddCommand(e.runCmd(), e.lsCmd(), e.extendCmd(), e.rmCmd())
	return root
}

func (e *env) runCmd() *cobra.Command {
	var (
		name string
		ttl  time.Duration
		it   bool
	)
	cmd := &cobra.Command{
		Use:   "run <image> [--ttl 2h] [--name N] [-it] [-- cmd args...]",
		Short: "Create an OnDemandPod, wait for it to be ready, optionally open a shell",
		Long: `Equivalent to creating an OnDemandPod with kubectl and waiting for Ready.
Arguments after -- become the container command (first word) and args.
With -it the plugin then execs "sh" in the container.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, cfg, ns, err := e.connect()
			if err != nil {
				return err
			}
			opts := plugin.RunOptions{Image: args[0], Name: name}
			if cmd.Flags().Changed("ttl") {
				opts.TTL = &ttl
			}
			if dash := cmd.ArgsLenAtDash(); dash >= 0 {
				if dash == 0 {
					return errors.New("an image is required before --")
				}
				opts.Command = args[dash:]
			}
			odp, err := plugin.Create(cmd.Context(), c, ns, opts)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "onDemandPod/%s created\n", odp.Name)
			key := types.NamespacedName{Namespace: ns, Name: odp.Name}
			_, err = plugin.WaitReady(cmd.Context(), c, key, pollInterval, func(reason, msg string) {
				fmt.Fprintf(os.Stderr, "waiting: %s %s\n", reason, msg)
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "onDemandPod/%s is ready\n", odp.Name)
			if !it {
				return nil
			}
			return plugin.Exec(cmd.Context(), cfg, plugin.ExecOptions{
				Namespace: ns, Pod: odp.Name, Command: []string{"sh"},
				In: os.Stdin, Out: os.Stdout, Err: os.Stderr,
			})
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Name of the OnDemandPod (generated when empty)")
	cmd.Flags().DurationVar(&ttl, "ttl", 0, "Lease length (the operator default applies when unset)")
	// -i and -t are separate flags so that -it works as it does in kubectl;
	// either one asks for a shell.
	cmd.Flags().BoolVarP(&it, "stdin", "i", false, "Open a shell once the pod is ready")
	cmd.Flags().BoolVarP(&it, "tty", "t", false, "Open a shell once the pod is ready")
	return cmd
}

func (e *env) lsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List OnDemandPods with the time remaining on each lease",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, _, ns, err := e.connect()
			if err != nil {
				return err
			}
			rows, err := plugin.List(cmd.Context(), c, ns, time.Now())
			if err != nil {
				return err
			}
			return plugin.WriteTable(os.Stdout, rows)
		},
	}
}

func (e *env) extendCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "extend <name> <duration>",
		Short: "Set spec.ttl, the total lease length measured from creation",
		Long: `Equivalent to patching spec.ttl. The duration is the new total lease length,
not an increment: extending a 1h lease to 3h gives it 3h from creation.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := time.ParseDuration(args[1])
			if err != nil {
				return fmt.Errorf("invalid duration %q: %w", args[1], err)
			}
			c, _, ns, err := e.connect()
			if err != nil {
				return err
			}
			key := types.NamespacedName{Namespace: ns, Name: args[0]}
			res, err := plugin.Extend(cmd.Context(), c, key, d, pollInterval, observeTimeout)
			if err != nil {
				return err
			}
			switch {
			case !res.Observed:
				fmt.Fprintln(os.Stderr, "warning: the operator has not confirmed the new ttl yet; check `kubectl ondemand ls`")
			case res.Clamped:
				fmt.Fprintf(os.Stderr, "warning: ttl exceeds the operator maximum and was clamped; "+
					"the lease ends at %s\n", res.ExpiresAt.Format(time.RFC3339))
			default:
				fmt.Fprintf(os.Stderr, "onDemandPod/%s now expires at %s\n", args[0], res.ExpiresAt.Format(time.RFC3339))
			}
			return nil
		},
	}
}

func (e *env) rmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <name>",
		Short: "Delete an OnDemandPod, and with it its Pod",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, ns, err := e.connect()
			if err != nil {
				return err
			}
			if err := plugin.Remove(cmd.Context(), c, types.NamespacedName{Namespace: ns, Name: args[0]}); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "onDemandPod/%s deleted\n", args[0])
			return nil
		},
	}
}
