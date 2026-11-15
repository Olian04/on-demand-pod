package plugin

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"

	v1alpha1 "github.com/Olian04/on-demand-pod/api/v1alpha1"
)

// ExecOptions is a `kubectl exec -it` of the operator's container.
type ExecOptions struct {
	Namespace, Pod string
	Command        []string
	In             *os.File
	Out, Err       io.Writer
}

// Exec runs a command in the Pod's only container with a TTY. It is the one
// part of the plugin that cannot be integration-tested without a kubelet.
func Exec(ctx context.Context, cfg *rest.Config, o ExecOptions) error {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	req := cs.CoreV1().RESTClient().Post().Resource("pods").Namespace(o.Namespace).Name(o.Pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: v1alpha1.ContainerName,
			Command:   o.Command,
			Stdin:     true,
			Stdout:    true,
			TTY:       true,
		}, scheme.ParameterCodec)

	spdy, err := remotecommand.NewSPDYExecutor(cfg, http.MethodPost, req.URL())
	if err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	ws, err := remotecommand.NewWebSocketExecutor(cfg, http.MethodGet, req.URL().String())
	if err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	// Prefer WebSockets and fall back to SPDY, as kubectl does.
	exec, err := remotecommand.NewFallbackExecutor(ws, spdy, func(error) bool { return true })
	if err != nil {
		return fmt.Errorf("exec: %w", err)
	}

	fd := int(o.In.Fd())
	if term.IsTerminal(fd) {
		state, err := term.MakeRaw(fd)
		if err != nil {
			return fmt.Errorf("raw terminal: %w", err)
		}
		defer func() { _ = term.Restore(fd, state) }()
	}
	sizes := newSizeQueue(ctx, fd)
	return exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin: o.In, Stdout: o.Out, Tty: true, TerminalSizeQueue: sizes,
	})
}

type sizeQueue struct {
	ch chan remotecommand.TerminalSize
}

func newSizeQueue(ctx context.Context, fd int) *sizeQueue {
	q := &sizeQueue{ch: make(chan remotecommand.TerminalSize, 1)}
	push := func() {
		if w, h, err := term.GetSize(fd); err == nil {
			select {
			case q.ch <- remotecommand.TerminalSize{Width: uint16(w), Height: uint16(h)}:
			default:
			}
		}
	}
	push()
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	go func() {
		defer signal.Stop(winch)
		// Closing ends the executor's Next loop so it cannot outlive ctx.
		defer close(q.ch)
		for {
			select {
			case <-ctx.Done():
				return
			case <-winch:
				push()
			}
		}
	}()
	return q
}

// Next implements remotecommand.TerminalSizeQueue.
func (q *sizeQueue) Next() *remotecommand.TerminalSize {
	s, ok := <-q.ch
	if !ok {
		return nil
	}
	return &s
}
