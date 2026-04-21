package main

// The previous TestApplyUsesToContextExactly stubbed `kubectl` in PATH
// and asserted that gitups invoked it with `--context <name>`. That
// coupling no longer holds: gitups core does not hard-code kubectl or
// its argv. The KRC's spec.cli.intents templates own every command
// line — argocd's `apply` intent in gitups-packages happens to call
// kubectl, but a different KRC can declare any binary with any args
// shape. Verification of this wiring now lives in the e2e tests
// (tests/e2e/test5) that exercise apply against a real kind cluster
// using a real KRC package.
