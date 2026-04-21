// Cluster-state waits used by `gitups apply --wait-crds` and `gitups
// wait`. Every kubectl-equivalent read is routed through the KubeClient
// so gitups core carries no hard-coded cluster binary — the selected
// KRC package decides what to run.

package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
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
// Returns an error on timeout or on a CSV reaching Failed. Every read goes
// through kc so the cluster binary stays KRC-declared.
func WaitForSubscriptions(ctx context.Context, kc *KubeClient, subs []SubscriptionRef, opts WaitOptions) error {
	if len(subs) == 0 {
		return nil
	}
	if kc == nil {
		return fmt.Errorf("WaitForSubscriptions: KubeClient is nil")
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
		if err := waitOne(ctx, kc, s, deadline, opts); err != nil {
			return err
		}
	}
	return nil
}

func waitOne(ctx context.Context, kc *KubeClient, s SubscriptionRef, deadline time.Time, opts WaitOptions) error {
	fmt.Fprintf(opts.Out, "gitups: wait subscription %s/%s → installedCSV\n", s.Namespace, s.Name)
	var csv string
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout resolving installedCSV for subscription %s/%s", s.Namespace, s.Name)
		}
		body, err := kc.GetJSON(ctx, s.Namespace, "subscription", s.Name)
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
			surfaceCSVDiagnostics(ctx, kc, s.Namespace, csv, opts.Out)
			return fmt.Errorf("timeout waiting for csv %s/%s to Succeed", s.Namespace, csv)
		}
		body, err := kc.GetJSON(ctx, s.Namespace, "csv", csv)
		if err == nil {
			phase, reason := csvPhase(body)
			switch phase {
			case "Succeeded":
				fmt.Fprintf(opts.Out, "gitups: csv %s/%s Succeeded\n", s.Namespace, csv)
				return nil
			case "Failed":
				surfaceCSVDiagnostics(ctx, kc, s.Namespace, csv, opts.Out)
				return fmt.Errorf("csv %s/%s Failed: %s", s.Namespace, csv, reason)
			}
		}
		sleep(ctx, opts.Interval)
	}
}

// surfaceCSVDiagnostics prints the CSV's status phase/message plus the
// last 40 log lines of every pod labelled olm.owner=<csv>. Best-effort:
// swallows errors so the caller's own error path still reports the
// original timeout/Failed cause.
func surfaceCSVDiagnostics(ctx context.Context, kc *KubeClient, ns, csv string, out io.Writer) {
	fmt.Fprintf(out, "gitups: diagnostics for csv %s/%s:\n", ns, csv)
	if body, err := kc.GetJSON(ctx, ns, "csv", csv); err == nil {
		phase, reason := csvPhase(body)
		fmt.Fprintf(out, "gitups:   csv.status.phase=%q reason=%q\n", phase, reason)
	}
	selector := "olm.owner=" + csv
	podList, err := kc.ListPodsJSONPath(ctx, ns, selector)
	if err != nil || len(podList) == 0 {
		fmt.Fprintf(out, "gitups:   (no pods labelled %s in %s — skipping log tail)\n", selector, ns)
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(string(podList)), "\n") {
		parts := strings.SplitN(line, "|", 2)
		podName := parts[0]
		phase := ""
		if len(parts) > 1 {
			phase = parts[1]
		}
		if podName == "" {
			continue
		}
		fmt.Fprintf(out, "gitups:   pod %s phase=%s — last 40 lines:\n", podName, phase)
		body, _ := kc.PodLogs(ctx, ns, podName)
		for _, l := range strings.Split(strings.TrimRight(string(body), "\n"), "\n") {
			fmt.Fprintf(out, "gitups:     %s\n", l)
		}
	}
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
