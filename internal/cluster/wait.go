// Package cluster holds cluster-touching helpers shared between `gitups
// apply` and `gitups wait`. It shells to kubectl rather than pulling in
// client-go, keeping the binary small and dependency-light.
package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"time"

	v1 "github.com/crmarques/gitups/api/v1alpha1"
)

// SubscriptionRef identifies an OLM Subscription to wait on.
type SubscriptionRef struct {
	Namespace string
	Name      string // Subscription name (the gitups instance)
}

// SubscriptionsFromPackages returns the OLM subscription refs a FullProvision
// installs. Ordering matches fp.Spec.Packages. Packages with renderer != olm
// or missing resolvedValues.namespace are skipped silently.
func SubscriptionsFromPackages(pkgs []v1.ResolvedPackage) []SubscriptionRef {
	var out []SubscriptionRef
	for i := range pkgs {
		rp := &pkgs[i]
		if rp.Renderer != "olm" {
			continue
		}
		ns, _ := rp.ResolvedValues["namespace"].(string)
		if ns == "" {
			continue
		}
		out = append(out, SubscriptionRef{Namespace: ns, Name: rp.Instance})
	}
	return out
}

// SubscriptionsForRepo filters SubscriptionsFromPackages to a single repo.
func SubscriptionsForRepo(pkgs []v1.ResolvedPackage, repo string) []SubscriptionRef {
	var out []SubscriptionRef
	for i := range pkgs {
		rp := &pkgs[i]
		if rp.RenderedPaths.Repo != repo {
			continue
		}
		if rp.Renderer != "olm" {
			continue
		}
		ns, _ := rp.ResolvedValues["namespace"].(string)
		if ns == "" {
			continue
		}
		out = append(out, SubscriptionRef{Namespace: ns, Name: rp.Instance})
	}
	return out
}

// WaitOptions tunes the polling loop.
type WaitOptions struct {
	Timeout  time.Duration // overall budget across all subscriptions
	Interval time.Duration // poll cadence; defaults to 5s
	Out      io.Writer     // progress stream; nil → discard
}

// WaitForSubscriptions blocks until every subscription's .status.installedCSV
// is non-empty and the named ClusterServiceVersion reaches phase=Succeeded.
// Returns an error on timeout or on a CSV reaching Failed.
func WaitForSubscriptions(ctx context.Context, kubectlContext string, subs []SubscriptionRef, opts WaitOptions) error {
	if len(subs) == 0 {
		return nil
	}
	if opts.Interval <= 0 {
		opts.Interval = 5 * time.Second
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Minute
	}
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	deadline := time.Now().Add(opts.Timeout)
	for _, s := range subs {
		if err := waitOne(ctx, kubectlContext, s, deadline, opts); err != nil {
			return err
		}
	}
	return nil
}

func waitOne(ctx context.Context, kctx string, s SubscriptionRef, deadline time.Time, opts WaitOptions) error {
	fmt.Fprintf(opts.Out, "gitups: wait subscription %s/%s → installedCSV\n", s.Namespace, s.Name)
	var csv string
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout resolving installedCSV for subscription %s/%s", s.Namespace, s.Name)
		}
		body, err := kubectlGetJSON(ctx, kctx, s.Namespace, "subscription", s.Name)
		if err == nil {
			csv = subInstalledCSV(body)
			if csv != "" {
				break
			}
		}
		sleep(ctx, opts.Interval)
	}
	fmt.Fprintf(opts.Out, "gitups: wait csv %s/%s → Succeeded\n", s.Namespace, csv)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for csv %s/%s to Succeed", s.Namespace, csv)
		}
		body, err := kubectlGetJSON(ctx, kctx, s.Namespace, "csv", csv)
		if err == nil {
			phase, reason := csvPhase(body)
			switch phase {
			case "Succeeded":
				fmt.Fprintf(opts.Out, "gitups: csv %s/%s Succeeded\n", s.Namespace, csv)
				return nil
			case "Failed":
				return fmt.Errorf("csv %s/%s Failed: %s", s.Namespace, csv, reason)
			}
		}
		sleep(ctx, opts.Interval)
	}
}

func kubectlGetJSON(ctx context.Context, kctx, ns, kind, name string) ([]byte, error) {
	args := []string{"--context", kctx, "-n", ns, "get", kind, name, "-o", "json"}
	return exec.CommandContext(ctx, "kubectl", args...).Output()
}

// subInstalledCSV pulls .status.installedCSV from a Subscription JSON body.
// Returns "" if the field is absent.
func subInstalledCSV(body []byte) string {
	var d struct {
		Status struct {
			InstalledCSV string `json:"installedCSV"`
		} `json:"status"`
	}
	if json.Unmarshal(body, &d) != nil {
		return ""
	}
	return d.Status.InstalledCSV
}

// csvPhase pulls .status.phase and .status.reason from a CSV JSON body.
func csvPhase(body []byte) (phase, reason string) {
	var d struct {
		Status struct {
			Phase  string `json:"phase"`
			Reason string `json:"reason"`
		} `json:"status"`
	}
	if json.Unmarshal(body, &d) != nil {
		return "", ""
	}
	return d.Status.Phase, d.Status.Reason
}

// sleep pauses up to d, but returns early on ctx cancellation.
func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}
